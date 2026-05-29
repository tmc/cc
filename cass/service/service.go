package service

import (
	"context"
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
		&collector.OpenClaw{},
		&collector.Antigravity{},
		&collector.Cursor{},
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

// Index runs all collectors and indexes found sessions.
// If force is true, it reindexes everything regardless of last scan time.
// Returns the number of sessions indexed.
func (s *Service) Index(ctx context.Context, force bool, extraPaths ...string) (int, error) {
	var since time.Time
	if !force {
		if ts, err := s.store.Meta(ctx, "last_indexed_at"); err == nil && ts != "" {
			if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
				since = time.Unix(unix, 0)
			}
		}
	}

	var (
		total int
		mu    sync.Mutex
		write sync.Mutex
		wg    sync.WaitGroup
	)

	for _, c := range s.collectors {
		var paths []string
		det, err := c.Detect(ctx)
		if err == nil && det != nil && det.Found {
			paths = append(paths, det.Paths...)
		}

		// Support passing arbitrary extra paths from the CLI to capable collectors.
		if c.Name() == "claude-code" || c.Name() == "gemini-cli" || c.Name() == "codex" {
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
					write.Lock()
					if err := s.store.BatchIndex(ctx, batch); err != nil {
						s.log.Error("batch index", "agent", col.Name(), "err", err)
					}
					write.Unlock()
					mu.Lock()
					total += len(batch)
					mu.Unlock()
					batch = batch[:0]
				}
			}

			// Flush remaining.
			if len(batch) > 0 {
				write.Lock()
				if err := s.store.BatchIndex(ctx, batch); err != nil {
					s.log.Error("batch index", "agent", col.Name(), "err", err)
				}
				write.Unlock()
				mu.Lock()
				total += len(batch)
				mu.Unlock()
			}

			if scanErr := <-errCh; scanErr != nil {
				s.log.Error("scan", "agent", col.Name(), "err", scanErr)
			}
		}(c, paths)
	}

	wg.Wait()

	// Record last indexed time.
	if err := s.store.SetMeta(ctx, "last_indexed_at", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		s.log.Warn("set last_indexed_at", "err", err)
	}

	return total, nil
}

// Search queries the index.
func (s *Service) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	return s.store.Search(ctx, req)
}

// SourcePath returns the source file path for a session by its ID.
func (s *Service) SourcePath(ctx context.Context, id string) (string, error) {
	return s.store.SourcePath(ctx, id)
}

// Session returns indexed metadata for a session.
func (s *Service) Session(ctx context.Context, id string) (cass.Hit, error) {
	return s.store.Session(ctx, id)
}

// Stats returns basic index statistics.
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	count, err := s.store.SessionCount(ctx)
	if err != nil {
		return nil, err
	}
	lastIndexed, _ := s.store.Meta(ctx, "last_indexed_at")
	return map[string]any{
		"session_count": count,
		"last_indexed":  lastIndexed,
	}, nil
}

// AggregateStats returns detailed aggregate statistics, optionally filtered by time range.
// AggregateStats returns denormalized index-wide counters for the [after,
// before] window. The query is a multi-column SUM over every session row, so it
// is the single most expensive read in the API; the web UI fans out several
// identical requests per refresh across panels. Concurrent calls for the same
// window are coalesced through a singleflight group so only one query hits
// SQLite and the rest share its result. Each caller still observes its own
// context: a cancellation returns promptly without aborting the shared query
// the other waiters depend on.
func (s *Service) AggregateStats(ctx context.Context, after, before time.Time) (map[string]any, error) {
	key := after.UTC().Format(time.RFC3339Nano) + "|" + before.UTC().Format(time.RFC3339Nano)
	ch := s.statsSF.DoChan(key, func() (any, error) {
		// Use a detached context so the shared query is not bound to the first
		// caller's request lifetime; waiters select on their own ctx below.
		return s.store.AggregateStats(context.WithoutCancel(ctx), after, before)
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
		if err := s.store.BatchIndex(ctx, batch); err != nil {
			return 0, fmt.Errorf("index %s: %w", g.collector.Name(), err)
		}
		total += len(batch)
	}

	return total, nil
}

// IndexPaths re-indexes session files by their parent directories.
// Used by the file watcher for incremental updates without a full scan.
func (s *Service) IndexPaths(ctx context.Context, filePaths []string) (int, error) {
	type pathGroup struct {
		collector cass.Collector
		dirs      map[string]struct{}
	}

	groups := map[string]*pathGroup{}
	for _, path := range filePaths {
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
		if err := s.store.BatchIndex(ctx, batch); err != nil {
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
	case strings.Contains(path, sep+".gemini"+sep):
		return &collector.GeminiCLI{}
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

	s.log.Info("artifact dir scan", "root", root, "requests", len(requests))

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

	s.log.Info("sessionv2 scan", "path", path, "requests", len(requests))

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

	s.log.Info("har scan", "dir", dir, "requests", len(requests))

	// Index API requests.
	if err := s.store.BatchIndexRequests(ctx, requests); err != nil {
		return 0, fmt.Errorf("index har requests: %w", err)
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

// QueryRequests returns API requests linked to a session.
func (s *Service) QueryRequests(ctx context.Context, sessionID string) ([]cass.APIRequest, error) {
	return s.store.QueryRequests(ctx, sessionID)
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

// SubagentRuns lists Task subagent invocations matching the given filter.
func (s *Service) SubagentRuns(ctx context.Context, f store.SubagentRunFilter) ([]store.SubagentRunListEntry, error) {
	return s.store.SubagentRuns(ctx, f)
}

// SubagentRunsSummary returns aggregate counts and per-agent-type histogram.
func (s *Service) SubagentRunsSummary(ctx context.Context) (store.SubagentRunSummary, error) {
	return s.store.SubagentRunsSummary(ctx)
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
