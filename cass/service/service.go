package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/collector"
	"github.com/tmc/cc/cass/collector/har"
	"github.com/tmc/cc/cass/store"
)

// Config configures the service.
type Config struct {
	DBPath     string // Path to the SQLite database. Defaults to ~/.cache/cass/index.db.
	Collectors []cass.Collector
	Logger     *slog.Logger

	// ParseCacheBytes bounds the in-memory incremental-parse cache used by
	// IndexPaths (the long-lived server's file-watcher path). Zero selects a
	// default; a negative value disables caching so every reindex reads in
	// full. One-shot Index never consults the cache regardless.
	ParseCacheBytes int64
}

// defaultParseCacheBytes caps the incremental-parse cache. Large enough to hold
// the working set of actively-written sessions; evicting a giant session just
// makes its next reindex a full read.
const defaultParseCacheBytes = 1500 << 20 // ~1.5 GiB

// Service orchestrates collection, indexing, and search.
type Service struct {
	store      *store.DB
	collectors []cass.Collector
	log        *slog.Logger
	cache      *ParseCache
	statsSF    singleflight.Group // coalesces concurrent identical AggregateStats queries.

	statsMu    sync.Mutex                 // guards statsCache.
	statsCache map[string]cachedAggregate // memoizes AggregateStats results by window, keyed as in AggregateStats.
}

// aggregateTTL bounds how long an AggregateStats result is served from
// statsCache before the underlying scan is repeated. The query is a full-table
// SUM over every session row (the single most expensive read in the API), so on
// a large index it costs tens of seconds; a short TTL lets the common all-time
// query — fired by several UI panels on every refresh — run at most once per
// window per interval. Staleness is bounded to the TTL: sessions indexed within
// the last few seconds may be absent from the totals, which is invisible in a
// live-updating dashboard.
const aggregateTTL = 30 * time.Second

// cachedAggregate is a memoized AggregateStats result with its computation time.
type cachedAggregate struct {
	at    time.Time
	stats map[string]any
}

// New creates a new service with the given configuration.
func New(cfg Config) (*Service, error) {
	if cfg.DBPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		cfg.DBPath = filepath.Join(home, ".cache", "cass", "index.db")
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	st, err := store.New(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	collectors := cfg.Collectors
	if len(collectors) == 0 {
		collectors = defaultCollectors()
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	cacheBytes := cfg.ParseCacheBytes
	if cacheBytes == 0 {
		cacheBytes = defaultParseCacheBytes
	}

	return &Service{
		store:      st,
		collectors: collectors,
		log:        logger,
		cache:      NewParseCache(cacheBytes),
	}, nil
}

func defaultCollectors() []cass.Collector {
	return []cass.Collector{
		&collector.ClaudeCode{},
		&collector.GeminiCLI{}, // Added for Gemini CLI support
		&collector.Codex{},
		&collector.OpenCode{},
		&collector.OpenClaw{},
		&collector.Antigravity{},
		&collector.Cursor{},
		&collector.Pi{},
	}
}

// Detect runs detection on all registered collectors.
func (s *Service) Detect(ctx context.Context) ([]cass.DetectionResult, error) {
	var results []cass.DetectionResult
	for _, c := range s.collectors {
		r, err := c.Detect(ctx)
		if err != nil {
			s.log.Warn("detect failed", "agent", c.Name(), "err", err)
			continue
		}
		results = append(results, *r)
	}
	return results, nil
}

// scanSince returns the modified-since floor for an incremental scan given the
// stored last_indexed_at value. It returns the zero time (full walk) when the
// cursor is missing, unparseable, or dated in the future, which a clock jump can
// leave behind and which would otherwise skip every file until the clock catches
// up. The cursor is only ever written after a walk that completed without error,
// so an old cursor is trustworthy: it names ground already fully covered.
func scanSince(meta string, now time.Time) time.Time {
	unix, err := strconv.ParseInt(meta, 10, 64)
	if err != nil {
		return time.Time{}
	}
	since := time.Unix(unix, 0)
	if now.Before(since) {
		return time.Time{}
	}
	return since
}

// Index runs all collectors and indexes found sessions.
// If force is true, it reindexes everything regardless of last scan time.
// Returns the number of sessions indexed.
//
// last_indexed_at advances only when every collector detected, scanned, and
// indexed without error. A partial walk that stamped the cursor would claim
// coverage it does not have, and every file it missed with an older mtime would
// stay invisible to later incremental runs. Index reports such failures to the
// caller rather than only logging them, alongside the sessions it did index.
func (s *Service) Index(ctx context.Context, force bool, extraPaths ...string) (int, error) {
	// The cursor records when the walk started, not when it ended: a file
	// written while the walk was already past its directory must still look
	// newer than the cursor to the next run.
	start := time.Now()
	attr := newRepoAttributor(ctx, s.store)

	var since time.Time
	if !force {
		if ts, err := s.store.Meta(ctx, "last_indexed_at"); err == nil && ts != "" {
			since = scanSince(ts, start)
		}
	}

	var (
		total int
		errs  []error
		mu    sync.Mutex
		write sync.Mutex
		wg    sync.WaitGroup
	)

	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	for _, c := range s.collectors {
		var paths []string
		det, err := c.Detect(ctx)
		switch {
		case err != nil:
			s.log.Error("detect", "agent", c.Name(), "err", err)
			fail(fmt.Errorf("detect %s: %w", c.Name(), err))
		case det != nil && det.Found:
			paths = append(paths, det.Paths...)
		}

		// Support passing arbitrary extra paths from the CLI to capable collectors.
		if c.Name() == "claude-code" || c.Name() == "gemini-cli" || c.Name() == "codex" || c.Name() == "opencode" {
			paths = append(paths, extraPaths...)
		}

		if len(paths) == 0 {
			continue
		}

		wg.Add(1)
		go func(col cass.Collector, paths []string) {
			defer wg.Done()

			ch := make(chan cass.Session, 100)
			errCh := make(chan error, 1)

			go func() {
				errCh <- col.Scan(ctx, cass.ScanConfig{
					Paths: paths,
					Since: since,
				}, ch)
			}()

			var batch []cass.Session
			for sess := range ch {
				batch = append(batch, sess)
				if len(batch) >= 100 {
					// Attribution forks git and is guarded by its own
					// lock, so it runs outside the write lock: one
					// collector's git work overlaps another's writes.
					attr.attribute(batch)
					write.Lock()
					err := s.indexBatch(ctx, attr, batch)
					write.Unlock()
					if err != nil {
						s.log.Error("batch index", "agent", col.Name(), "err", err)
						fail(fmt.Errorf("index %s: %w", col.Name(), err))
					} else {
						mu.Lock()
						total += len(batch)
						mu.Unlock()
					}
					batch = batch[:0]
				}
			}

			// Flush remaining.
			if len(batch) > 0 {
				attr.attribute(batch)
				write.Lock()
				err := s.indexBatch(ctx, attr, batch)
				write.Unlock()
				if err != nil {
					s.log.Error("batch index", "agent", col.Name(), "err", err)
					fail(fmt.Errorf("index %s: %w", col.Name(), err))
				} else {
					mu.Lock()
					total += len(batch)
					mu.Unlock()
				}
			}

			if scanErr := <-errCh; scanErr != nil {
				s.log.Error("scan", "agent", col.Name(), "err", scanErr)
				fail(fmt.Errorf("scan %s: %w", col.Name(), scanErr))
			}
		}(c, paths)
	}

	wg.Wait()

	if len(errs) > 0 {
		return total, errors.Join(errs...)
	}

	// Record last indexed time. Only reached by a walk that covered every
	// detected path, so the cursor is a truthful coverage claim.
	if err := s.store.SetMeta(ctx, "last_indexed_at", strconv.FormatInt(start.Unix(), 10)); err != nil {
		return total, fmt.Errorf("set last_indexed_at: %w", err)
	}

	return total, nil
}

// Search queries the index.
func (s *Service) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	return s.search(ctx, req, true)
}

// SearchSummary queries the index without read-time artifact enrichment.
// Callers use it for lightweight lists, then fetch Session for detail.
func (s *Service) SearchSummary(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	req.SummaryOnly = true
	return s.search(ctx, req, false)
}

func (s *Service) search(ctx context.Context, req cass.SearchRequest, enrich bool) (*cass.SearchResult, error) {
	result, err := s.store.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	if enrich {
		for i := range result.Hits {
			enrichHitWorkflowMetadata(&result.Hits[i])
		}
	} else {
		s.retitleWorkflowCommandHits(ctx, result.Hits)
	}
	return result, nil
}

// SourcePath returns the source file path for a session by its ID.
func (s *Service) SourcePath(ctx context.Context, id string) (string, error) {
	return s.store.SourcePath(ctx, id)
}

// Session returns indexed metadata for a session.
func (s *Service) Session(ctx context.Context, id string) (cass.Hit, error) {
	hit, err := s.store.Session(ctx, id)
	if err != nil {
		return cass.Hit{}, err
	}
	enrichHitWorkflowMetadata(&hit)
	return hit, nil
}

func enrichHitWorkflowMetadata(hit *cass.Hit) {
	if hit == nil {
		return
	}
	for i := range hit.Workflows {
		collector.EnrichWorkflowMetadata(&hit.Workflows[i])
	}
	retitleWorkflowCommandHit(hit)
}

func retitleWorkflowCommandHit(hit *cass.Hit) {
	if hit == nil || !workflowCommandTitle(hit.Title) {
		return
	}
	if title := collector.WorkflowSessionTitle(hit.Workflows); title != "" {
		hit.Title = title
	}
}

func (s *Service) retitleWorkflowCommandHits(ctx context.Context, hits []cass.Hit) {
	need := make([]string, 0, len(hits))
	needByID := map[string]int{}
	for i := range hits {
		if !workflowCommandTitle(hits[i].Title) {
			continue
		}
		needByID[hits[i].SessionID] = i
		need = append(need, hits[i].SessionID)
	}
	rowsByParent, err := s.store.WorkflowsByParentIDs(ctx, need)
	if err != nil {
		return
	}
	for parentID, i := range needByID {
		rows := rowsByParent[parentID]
		if len(rows) == 0 {
			continue
		}
		workflows := make([]cass.WorkflowRun, 0, len(rows))
		for _, row := range rows {
			workflows = append(workflows, cass.WorkflowRun{
				RunID:       row.RunID,
				Name:        row.Name,
				Description: row.Description,
				Summary:     row.Summary,
			})
		}
		if title := collector.WorkflowSessionTitle(workflows); title != "" {
			hits[i].Title = title
		}
	}
}

func workflowCommandTitle(title string) bool {
	title = strings.ToLower(title)
	return strings.Contains(title, "<command-name>") && strings.Contains(title, "</command-name>")
}

// Stats returns basic index statistics.
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	count, err := s.store.SessionCount(ctx)
	if err != nil {
		return nil, err
	}
	lastIndexed, _ := s.store.Meta(ctx, "last_indexed_at")
	reqCount, err := s.store.APIRequestCount(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"session_count":     count,
		"last_indexed":      lastIndexed,
		"api_request_count": reqCount,
	}, nil
}

// AggregateStats returns denormalized index-wide counters for the [after,
// before] window. The query is a multi-column SUM over every session row, so it
// is the single most expensive read in the API; the web UI fans out several
// identical requests per refresh across panels.
//
// Two layers keep that cost down. A result memoized within aggregateTTL is
// returned directly, so repeated refreshes of a stable window (the common
// all-time query) run the scan at most once per interval. On a miss, concurrent
// calls for the same window are coalesced through a singleflight group so only
// one query hits SQLite and the rest share its result. Each caller still
// observes its own context: a cancellation returns promptly without aborting the
// shared query the other waiters depend on.
func (s *Service) AggregateStats(ctx context.Context, after, before time.Time) (map[string]any, error) {
	key := after.UTC().Format(time.RFC3339Nano) + "|" + before.UTC().Format(time.RFC3339Nano)

	now := time.Now()
	s.statsMu.Lock()
	if c, ok := s.statsCache[key]; ok && now.Sub(c.at) < aggregateTTL {
		s.statsMu.Unlock()
		return c.stats, nil
	}
	s.statsMu.Unlock()

	ch := s.statsSF.DoChan(key, func() (any, error) {
		// Use a detached context so the shared query is not bound to the first
		// caller's request lifetime; waiters select on their own ctx below.
		stats, err := s.store.AggregateStats(context.WithoutCancel(ctx), after, before)
		if err != nil {
			return nil, err
		}
		s.statsMu.Lock()
		if s.statsCache == nil {
			s.statsCache = make(map[string]cachedAggregate)
		}
		s.statsCache[key] = cachedAggregate{at: time.Now(), stats: stats}
		s.statsMu.Unlock()
		return stats, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(map[string]any), nil
	}
}

// Links returns session communication links.
func (s *Service) Links(ctx context.Context, sessionID string) ([]cass.SessionLink, error) {
	return s.store.Links(ctx, sessionID)
}

// Goals returns goal-mode objectives joined with parent session metadata.
func (s *Service) Goals(ctx context.Context, status string, limit int) ([]cass.GoalHit, error) {
	return s.store.Goals(ctx, status, limit)
}

// Skills returns skill usage records joined with parent session metadata.
func (s *Service) Skills(ctx context.Context, skill string, kind string, limit int) ([]cass.SkillHit, error) {
	return s.store.Skills(ctx, skill, kind, limit)
}

// Mappings returns iTerm2 <-> Claude session mappings.
func (s *Service) Mappings(ctx context.Context, filter string) ([]store.SessionMapping, error) {
	return s.store.Mappings(ctx, filter)
}

// ResolveLabels looks up human-readable labels for iTerm2 session ID prefixes.
func (s *Service) ResolveLabels(ctx context.Context, prefixes []string) (map[string]store.SessionLabel, error) {
	return s.store.ResolveLabels(ctx, prefixes)
}

// Graph returns combined node and link data for the session communication graph.
// The opts control workflow collapsing and node-type filtering.
func (s *Service) Graph(ctx context.Context, since time.Time, opts cass.GraphOptions) (*cass.GraphData, error) {
	return s.store.GraphDataOpts(ctx, since, opts)
}

// IndexRoots indexes explicit session roots without running agent detection.
// It is for targeted imports and checks that should not walk all session history.
func (s *Service) IndexRoots(ctx context.Context, paths []string) (int, error) {
	attr := newRepoAttributor(ctx, s.store)

	type pathGroup struct {
		collector cass.Collector
		paths     []string
	}

	groups := map[string]*pathGroup{}
	for _, path := range paths {
		col := collectorForPath(path)
		if col.Name() == "claude-code" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				path = filepath.Dir(path)
			}
		}
		name := col.Name()
		g := groups[name]
		if g == nil {
			g = &pathGroup{collector: col}
			groups[name] = g
		}
		g.paths = append(g.paths, path)
	}

	total := 0
	for _, g := range groups {
		ch := make(chan cass.Session, 64)
		errCh := make(chan error, 1)
		go func(col cass.Collector, paths []string) {
			errCh <- col.Scan(ctx, cass.ScanConfig{Paths: paths}, ch)
		}(g.collector, g.paths)

		var batch []cass.Session
		for sess := range ch {
			batch = append(batch, sess)
		}
		if scanErr := <-errCh; scanErr != nil {
			return 0, fmt.Errorf("scan %s: %w", g.collector.Name(), scanErr)
		}
		if len(batch) == 0 {
			continue
		}
		attr.attribute(batch)
		if err := s.indexBatch(ctx, attr, batch); err != nil {
			return 0, fmt.Errorf("index %s: %w", g.collector.Name(), err)
		}
		total += len(batch)
	}

	return total, nil
}

// IndexPaths re-indexes session files by their parent directories.
// Used by the file watcher for incremental updates without a full scan.
// ParentSessionPath maps a subagent or workflow-agent transcript path back to
// its parent session JSONL by collapsing from the first "subagents" path
// segment. It handles both the flat layout
// (<uuid>/subagents/agent-x.jsonl) and the nested workflow layout
// (<uuid>/subagents/workflows/<run_id>/agent-x.jsonl). A parent session path is
// returned unchanged. The two depths must collapse to the same parent, or the
// watcher reindexes a directory the collector deliberately emits nothing from.
func ParentSessionPath(p string) string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	for i, seg := range parts {
		if seg == "subagents" && i > 0 {
			return filepath.FromSlash(strings.Join(parts[:i], "/")) + ".jsonl"
		}
	}
	return p
}

func (s *Service) IndexPaths(ctx context.Context, filePaths []string) (int, error) {
	attr := newRepoAttributor(ctx, s.store)

	type pathGroup struct {
		collector cass.Collector
		dirs      map[string]struct{}
	}

	groups := map[string]*pathGroup{}
	for _, path := range filePaths {
		// Map a subagent/workflow-agent transcript back to its parent session
		// file: those are folded into the parent, never indexed standalone, and
		// rooting the walk inside subagents/ would index nothing. This makes the
		// parent's folded agent content refresh when only an agent file changes.
		path = ParentSessionPath(path)
		col := collectorForPath(path)
		name := col.Name()
		g := groups[name]
		if g == nil {
			g = &pathGroup{
				collector: col,
				dirs:      map[string]struct{}{},
			}
			groups[name] = g
		}
		g.dirs[filepath.Dir(path)] = struct{}{}
	}

	// Scan walks each changed file's whole project directory, so without a
	// modified-since floor it re-parses every session in that directory on
	// every change. Bound it to the oldest changed file's mtime (less a small
	// margin for clock skew and parent-vs-subagent write ordering) so the walk
	// skips sessions untouched by this batch.
	since := changedFilesSince(filePaths)

	total := 0
	for _, g := range groups {
		dirList := make([]string, 0, len(g.dirs))
		for dir := range g.dirs {
			dirList = append(dirList, dir)
		}

		ch := make(chan cass.Session, 64)
		errCh := make(chan error, 1)
		go func(col cass.Collector, paths []string) {
			// Parse via the incremental cache: skip unchanged files, tail only
			// the appended bytes of grown ones. Only IndexPaths (the long-lived
			// server's watcher path) uses the cache; one-shot Index does not.
			errCh <- col.Scan(ctx, cass.ScanConfig{Paths: paths, Since: since, Parse: s.cache.ParseFile}, ch)
		}(g.collector, dirList)

		var batch []cass.Session
		for sess := range ch {
			batch = append(batch, sess)
		}
		if scanErr := <-errCh; scanErr != nil {
			return 0, fmt.Errorf("scan %s: %w", g.collector.Name(), scanErr)
		}
		if len(batch) == 0 {
			continue
		}
		attr.attribute(batch)
		if err := s.indexBatch(ctx, attr, batch); err != nil {
			return 0, fmt.Errorf("index paths: %w", err)
		}
		total += len(batch)
	}

	return total, nil
}

// changedFilesSince returns a modified-since floor for re-indexing: the oldest
// mtime among the changed files, less a 5s margin to tolerate clock skew and
// the parent session being flushed slightly before or after the subagent file
// that triggered the change. Returns the zero time (no filter) if no mtime can
// be read, preserving the previous full-walk behavior as a safe fallback.
func changedFilesSince(filePaths []string) time.Time {
	var oldest time.Time
	for _, p := range filePaths {
		fi, err := os.Stat(p)
		if err != nil {
			// A path we cannot stat may still be reindex-worthy (e.g. a parent
			// derived from a subagent path); fall back to no floor.
			return time.Time{}
		}
		if oldest.IsZero() || fi.ModTime().Before(oldest) {
			oldest = fi.ModTime()
		}
	}
	if oldest.IsZero() {
		return time.Time{}
	}
	return oldest.Add(-5 * time.Second)
}

func collectorForPath(path string) cass.Collector {
	path = strings.ToLower(path)
	sep := string(filepath.Separator)
	switch {
	case strings.Contains(path, sep+".codex"+sep):
		return &collector.Codex{}
	case strings.Contains(path, sep+"opencode"+sep):
		return &collector.OpenCode{}
	case strings.Contains(path, sep+".gemini"+sep):
		return &collector.GeminiCLI{}
	case strings.Contains(path, sep+".pi"+sep) || strings.Contains(path, sep+"agent"+sep+"sessions"+sep):
		return &collector.Pi{}
	default:
		return &collector.ClaudeCode{}
	}
}

// IndexArtifactDirs scans ~/.it2/sessions/*/proxy-traffic.*.jsonl files,
// parses each one as proxyman NDJSON, and indexes the resulting API requests
// and rate-limit snapshots. The it2 session UUID is extracted from the path.
// Pass an empty root to use the default ~/.it2/sessions location.
// Returns the number of API requests indexed.
func (s *Service) IndexArtifactDirs(ctx context.Context, root string) (int, error) {
	requests, err := har.ScanArtifactDirsContext(ctx, root)
	if err != nil {
		return 0, fmt.Errorf("scan artifact dirs: %w", err)
	}
	if len(requests) == 0 {
		return 0, nil
	}

	s.log.Info("artifact dir scan", "root", root, "api_requests", len(requests))

	if err := s.store.BatchIndexRequests(ctx, requests); err != nil {
		return 0, fmt.Errorf("index artifact requests: %w", err)
	}

	// Extract and store rate-limit snapshots.
	var snapshots []cass.RateLimitSnapshot
	for _, r := range requests {
		if r.RateLimits.Utilization5h > 0 || r.RateLimits.Utilization7d > 0 || r.RateLimits.ModelBucket != "" {
			snapshots = append(snapshots, r.RateLimits)
		}
	}
	if len(snapshots) > 0 {
		if err := s.store.SaveRateLimitSnapshots(ctx, snapshots); err != nil {
			s.log.Warn("save rate limit snapshots from artifacts", "err", err)
		}
	}

	return len(requests), nil
}

// IndexSessionV2 ingests .proxymansessionv2 or .proxymanlogv2 files, indexing
// the resulting API requests and rate-limit snapshots. path may be either a
// single file or a directory. Returns the number of API requests indexed.
func (s *Service) IndexSessionV2(ctx context.Context, path string) (int, error) {
	var requests []cass.APIRequest
	var err error

	info, statErr := os.Stat(path)
	if statErr != nil {
		return 0, fmt.Errorf("stat %s: %w", path, statErr)
	}
	if info.IsDir() {
		requests, err = har.ScanSessionV2Dir(path)
	} else {
		requests, err = har.ParseSessionV2File(path)
	}
	if err != nil {
		return 0, fmt.Errorf("scan sessionv2 %s: %w", path, err)
	}
	if len(requests) == 0 {
		return 0, nil
	}

	s.log.Info("sessionv2 scan", "path", path, "api_requests", len(requests))

	if err := s.store.BatchIndexRequests(ctx, requests); err != nil {
		return 0, fmt.Errorf("index sessionv2 requests: %w", err)
	}

	var snapshots []cass.RateLimitSnapshot
	for _, r := range requests {
		if r.RateLimits.Utilization5h > 0 || r.RateLimits.Utilization7d > 0 || r.RateLimits.ModelBucket != "" {
			snapshots = append(snapshots, r.RateLimits)
		}
	}
	if len(snapshots) > 0 {
		if err := s.store.SaveRateLimitSnapshots(ctx, snapshots); err != nil {
			s.log.Warn("save rate limit snapshots from sessionv2", "err", err)
		}
	}

	return len(requests), nil
}

// IndexHAR scans a directory of Proxyman HAR export files, parses each one,
// and indexes the resulting API requests and rate-limit snapshots.
// Returns the number of API requests indexed.
func (s *Service) IndexHAR(ctx context.Context, dir string) (int, error) {
	requests, err := har.ScanDirContext(ctx, dir)
	if err != nil {
		return 0, fmt.Errorf("scan har dir: %w", err)
	}
	if len(requests) == 0 {
		return 0, nil
	}

	s.log.Info("api request scan", "dir", dir, "api_requests", len(requests))

	// Index API requests.
	if err := s.store.BatchIndexRequests(ctx, requests); err != nil {
		return 0, fmt.Errorf("index api requests: %w", err)
	}

	// Extract and store rate-limit snapshots.
	var snapshots []cass.RateLimitSnapshot
	for _, r := range requests {
		if r.RateLimits.Utilization5h > 0 || r.RateLimits.Utilization7d > 0 || r.RateLimits.ModelBucket != "" {
			snapshots = append(snapshots, r.RateLimits)
		}
	}
	if len(snapshots) > 0 {
		if err := s.store.SaveRateLimitSnapshots(ctx, snapshots); err != nil {
			s.log.Warn("save rate limit snapshots", "err", err)
		}
	}

	return len(requests), nil
}

// QueryRequests returns API requests. If sessionID is non-empty, it returns
// requests linked to that session; otherwise it returns all requests.
func (s *Service) QueryRequests(ctx context.Context, sessionID string) ([]cass.APIRequest, error) {
	return s.store.QueryRequests(ctx, sessionID)
}

// QueryRequestsFiltered returns API requests matching f.
func (s *Service) QueryRequestsFiltered(ctx context.Context, f cass.APIRequestFilter) ([]cass.APIRequest, error) {
	return s.store.QueryRequestsFiltered(ctx, f)
}

// RateLimitTrend returns rate-limit utilization over time for a given bucket.
func (s *Service) RateLimitTrend(ctx context.Context, bucket string, since time.Time) ([]cass.RateLimitSnapshot, error) {
	return s.store.RateLimitTrend(ctx, bucket, since)
}

// APIRequestCount returns the number of indexed API requests.
func (s *Service) APIRequestCount(ctx context.Context) (int, error) {
	return s.store.APIRequestCount(ctx)
}

// DailyTokenUsage returns per-day token totals from indexed API requests.
func (s *Service) DailyTokenUsage(ctx context.Context, after time.Time) ([]store.DailyTokenRow, error) {
	return s.store.DailyTokenUsage(ctx, after)
}

// IndexTeamConfigs scans ~/.claude/teams/*/config.json and indexes each config.
// Returns the number of team configs indexed.
func (s *Service) IndexTeamConfigs(ctx context.Context, root string) (int, error) {
	configs, err := collector.ScanTeamConfigs(root)
	if err != nil {
		return 0, fmt.Errorf("scan team configs: %w", err)
	}
	for _, tc := range configs {
		if err := s.store.SaveTeamConfig(ctx, tc); err != nil {
			s.log.Warn("save team config", "name", tc.Name, "err", err)
		}
	}
	return len(configs), nil
}

// TeamConfigs returns all indexed team configurations.
func (s *Service) TeamConfigs(ctx context.Context) ([]store.TeamConfig, error) {
	return s.store.TeamConfigs(ctx)
}

// TeamMembers returns normalized members for an indexed team. If teamName is
// empty, it returns members for every indexed team.
func (s *Service) TeamMembers(ctx context.Context, teamName string) ([]store.TeamMember, error) {
	return s.store.TeamMembers(ctx, teamName)
}

// SubagentRuns lists Task subagent invocations matching the given filter.
func (s *Service) SubagentRuns(ctx context.Context, f store.SubagentRunFilter) ([]store.SubagentRunListEntry, error) {
	return s.store.SubagentRuns(ctx, f)
}

// SubagentRunsSummary returns aggregate counts and per-agent-type histogram.
func (s *Service) SubagentRunsSummary(ctx context.Context) (store.SubagentRunSummary, error) {
	return s.store.SubagentRunsSummary(ctx)
}

// Workflows lists indexed native workflow runs, optionally restricted to one
// parent session.
func (s *Service) Workflows(ctx context.Context, parentSessionID string) ([]store.WorkflowRow, error) {
	return s.store.Workflows(ctx, parentSessionID)
}

// WorkflowsSince lists indexed native workflow runs that started at or after
// since.
func (s *Service) WorkflowsSince(ctx context.Context, since time.Time) ([]store.WorkflowRow, error) {
	return s.store.WorkflowsSince(ctx, since)
}

// IndexJobs scans ~/.claude/jobs/<shortId>/state.json and upserts each job.
// Pass an empty root to use the default location. Returns the count indexed.
func (s *Service) IndexJobs(ctx context.Context, root string) (int, error) {
	jobs, err := collector.ScanJobs(root)
	if err != nil {
		return 0, fmt.Errorf("scan jobs: %w", err)
	}
	for _, j := range jobs {
		if err := s.store.SaveJob(ctx, j); err != nil {
			s.log.Warn("save job", "short_id", j.ShortID, "err", err)
		}
	}
	return len(jobs), nil
}

// Jobs returns all indexed jobs.
func (s *Service) Jobs(ctx context.Context) ([]store.Job, error) {
	return s.store.Jobs(ctx)
}

// IndexAgentDefs walks ~/.claude/agents and upserts each definition.
func (s *Service) IndexAgentDefs(ctx context.Context, root string) (int, error) {
	defs, err := collector.ScanAgentDefs(root)
	if err != nil {
		return 0, fmt.Errorf("scan agent defs: %w", err)
	}
	for _, a := range defs {
		if err := s.store.SaveAgentDef(ctx, a); err != nil {
			s.log.Warn("save agent def", "name", a.Name, "err", err)
		}
	}
	return len(defs), nil
}

// AgentDefs returns all indexed agent definitions.
func (s *Service) AgentDefs(ctx context.Context) ([]store.AgentDef, error) {
	return s.store.AgentDefs(ctx)
}

// Close releases resources.
func (s *Service) Close() error {
	return s.store.Close()
}

// indexBatch resolves each session's repository and writes the batch. Both
// steps belong to one call so that no path can persist usage events with no
// repository stamped on them.
// indexBatch writes an already-attributed batch. Callers run
// [repoAttributor.attribute] first, outside any write lock, because it forks
// git and would otherwise serialize every collector behind one.
func (s *Service) indexBatch(ctx context.Context, attr *repoAttributor, batch []cass.Session) error {
	if err := s.store.BatchIndex(ctx, batch); err != nil {
		return err
	}
	return attr.flush(ctx, s.store)
}

// UsageByHour returns per-UTC-hour token usage for the filter.
func (s *Service) UsageByHour(ctx context.Context, f store.UsageFilter) ([]store.HourRow, error) {
	return s.store.UsageByHour(ctx, f)
}

// UsageByGroup returns token usage per repository, agent, vendor and model.
func (s *Service) UsageByGroup(ctx context.Context, f store.UsageFilter) ([]store.GroupRow, error) {
	return s.store.UsageByGroup(ctx, f)
}

// UsageSessions returns the extent of each session with usage in the filter.
func (s *Service) UsageSessions(ctx context.Context, f store.UsageFilter) ([]store.SessionSpanRow, error) {
	return s.store.UsageSessions(ctx, f)
}

// UsageCoverage returns how many usage rows the index holds and the oldest one,
// for the warning the usage reports print on an index that predates the table.
func (s *Service) UsageCoverage(ctx context.Context) (int, time.Time, error) {
	return s.store.UsageEventCount(ctx)
}

// Repos returns every repository usage has been attributed to.
func (s *Service) Repos(ctx context.Context) (map[string]store.Repo, error) {
	return s.store.Repos(ctx)
}

// UsageSessionCounts returns the distinct session count per value of dim.
func (s *Service) UsageSessionCounts(ctx context.Context, f store.UsageFilter, dim string) (map[string]int, error) {
	return s.store.UsageSessionCounts(ctx, f, dim)
}

// UsageRowCounts returns the usage row count per value of dim.
func (s *Service) UsageRowCounts(ctx context.Context, f store.UsageFilter, dim string) (map[string]int, error) {
	return s.store.UsageRowCounts(ctx, f, dim)
}

// UsageBySession returns per-session usage totals for the filter.
func (s *Service) UsageBySession(ctx context.Context, f store.UsageFilter) ([]store.SessionUsageRow, error) {
	return s.store.UsageBySession(ctx, f)
}

// OldestSession returns the start of the earliest indexed session, which the
// usage reports compare against the oldest usage row to detect a stale index.
func (s *Service) OldestSession(ctx context.Context) (time.Time, error) {
	return s.store.OldestSessionStart(ctx)
}
