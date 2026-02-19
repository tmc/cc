package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

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
}

// Service orchestrates collection, indexing, and search.
type Service struct {
	store      *store.Store
	collectors []cass.Collector
	log        *slog.Logger
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

	return &Service{
		store:      st,
		collectors: collectors,
		log:        logger,
	}, nil
}

func defaultCollectors() []cass.Collector {
	return []cass.Collector{
		&collector.ClaudeCode{},
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
func (s *Service) Index(ctx context.Context, force bool) (int, error) {
	var since time.Time
	if !force {
		if ts, err := s.store.GetMeta(ctx, "last_indexed_at"); err == nil && ts != "" {
			if unix, err := strconv.ParseInt(ts, 10, 64); err == nil {
				since = time.Unix(unix, 0)
			}
		}
	}

	var (
		total int
		mu    sync.Mutex
		wg    sync.WaitGroup
	)

	for _, c := range s.collectors {
		det, err := c.Detect(ctx)
		if err != nil || !det.Found {
			continue
		}

		wg.Add(1)
		go func(col cass.Collector, paths []string) {
			defer wg.Done()

			ch := make(chan cass.Session, 100)
			var scanErr error

			go func() {
				scanErr = col.Scan(ctx, cass.ScanConfig{
					Paths: paths,
					Since: since,
				}, ch)
			}()

			var batch []cass.Session
			for sess := range ch {
				batch = append(batch, sess)
				if len(batch) >= 100 {
					if err := s.store.BatchIndex(ctx, batch); err != nil {
						s.log.Error("batch index", "agent", col.Name(), "err", err)
					}
					mu.Lock()
					total += len(batch)
					mu.Unlock()
					batch = batch[:0]
				}
			}

			// Flush remaining.
			if len(batch) > 0 {
				if err := s.store.BatchIndex(ctx, batch); err != nil {
					s.log.Error("batch index", "agent", col.Name(), "err", err)
				}
				mu.Lock()
				total += len(batch)
				mu.Unlock()
			}

			if scanErr != nil {
				s.log.Error("scan", "agent", col.Name(), "err", scanErr)
			}
		}(c, det.Paths)
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

// GetSourcePath returns the source file path for a session by its ID.
func (s *Service) GetSourcePath(ctx context.Context, id string) (string, error) {
	return s.store.GetSourcePath(ctx, id)
}

// Stats returns basic index statistics.
func (s *Service) Stats(ctx context.Context) (map[string]any, error) {
	count, err := s.store.SessionCount(ctx)
	if err != nil {
		return nil, err
	}
	lastIndexed, _ := s.store.GetMeta(ctx, "last_indexed_at")
	return map[string]any{
		"session_count": count,
		"last_indexed":  lastIndexed,
	}, nil
}

// AggregateStats returns detailed aggregate statistics, optionally filtered by time range.
func (s *Service) AggregateStats(ctx context.Context, after, before time.Time) (map[string]any, error) {
	return s.store.AggregateStats(ctx, after, before)
}

// Links returns session communication links.
func (s *Service) Links(ctx context.Context, sessionID string) ([]cass.SessionLink, error) {
	return s.store.Links(ctx, sessionID)
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
func (s *Service) Graph(ctx context.Context, since time.Time) (*cass.GraphData, error) {
	return s.store.GraphData(ctx, since)
}

// IndexPaths re-indexes session files by their parent directories.
// Used by the file watcher for incremental updates without a full scan.
func (s *Service) IndexPaths(ctx context.Context, filePaths []string) (int, error) {
	// Deduplicate parent directories.
	dirs := make(map[string]struct{})
	for _, p := range filePaths {
		dirs[filepath.Dir(p)] = struct{}{}
	}
	dirList := make([]string, 0, len(dirs))
	for d := range dirs {
		dirList = append(dirList, d)
	}

	c := &collector.ClaudeCode{}
	ch := make(chan cass.Session, 64)
	go func() {
		_ = c.Scan(ctx, cass.ScanConfig{Paths: dirList}, ch)
	}()

	var batch []cass.Session
	for sess := range ch {
		batch = append(batch, sess)
	}
	if len(batch) == 0 {
		return 0, nil
	}
	if err := s.store.BatchIndex(ctx, batch); err != nil {
		return 0, fmt.Errorf("index paths: %w", err)
	}
	return len(batch), nil
}

// IndexArtifactDirs scans ~/.it2/sessions/*/proxy-traffic.*.jsonl files,
// parses each one as proxyman NDJSON, and indexes the resulting API requests
// and rate-limit snapshots. The it2 session UUID is extracted from the path.
// Pass an empty root to use the default ~/.it2/sessions location.
// Returns the number of API requests indexed.
func (s *Service) IndexArtifactDirs(ctx context.Context, root string) (int, error) {
	requests, err := har.ScanArtifactDirs(root)
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

// IndexHAR scans a directory of Proxyman HAR export files, parses each one,
// and indexes the resulting API requests and rate-limit snapshots.
// Returns the number of API requests indexed.
func (s *Service) IndexHAR(ctx context.Context, dir string) (int, error) {
	requests, err := har.ScanDir(dir)
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

// Close releases resources.
func (s *Service) Close() error {
	return s.store.Close()
}
