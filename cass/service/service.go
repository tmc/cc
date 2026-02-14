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

// Close releases resources.
func (s *Service) Close() error {
	return s.store.Close()
}
