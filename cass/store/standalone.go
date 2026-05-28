package store

import (
	"context"

	"github.com/tmc/cc/cass"
)

// StandaloneBackend wraps a Backend and exposes just BatchIndex, Search,
// SessionCount, BackendStats, and Close. It is used for benchmarking and for
// the DuckDB prototype without touching the existing DB type.
type StandaloneBackend struct {
	b Backend
}

// NewWithConfig opens a StandaloneBackend using the given BackendConfig.
// This is the entry point for selecting between SQLite variants and DuckDB.
func NewWithConfig(cfg BackendConfig) (*StandaloneBackend, error) {
	b, err := openBackend(cfg)
	if err != nil {
		return nil, err
	}
	return &StandaloneBackend{b: b}, nil
}

// BatchIndex delegates to the underlying backend.
func (s *StandaloneBackend) BatchIndex(ctx context.Context, sessions []cass.Session) error {
	return s.b.BatchIndex(ctx, sessions)
}

// Search delegates to the underlying backend.
func (s *StandaloneBackend) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	return s.b.Search(ctx, req)
}

// SessionCount delegates to the underlying backend.
func (s *StandaloneBackend) SessionCount(ctx context.Context) (int, error) {
	return s.b.SessionCount(ctx)
}

// BackendStats returns index size metrics if the backend supports it.
func (s *StandaloneBackend) BackendStats(ctx context.Context) (Stats, error) {
	if st, ok := s.b.(Statter); ok {
		return st.BackendStats(ctx)
	}
	n, err := s.b.SessionCount(ctx)
	return Stats{TotalRows: int64(n)}, err
}

// Close releases the backend resources.
func (s *StandaloneBackend) Close() error {
	return s.b.Close()
}
