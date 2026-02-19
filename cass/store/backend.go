// Package store implements session storage backends for cass.
package store

import (
	"context"
	"time"

	"github.com/tmc/cc/cass"
)

// Backend is the minimal interface a storage engine must implement.
// The Store type wraps a Backend and exposes the full cass.Index plus
// supplementary methods (Links, GraphData, Stats, etc.).
//
// A Backend handles only the two hot-path operations—indexing and search—
// so that alternative engines (DuckDB, Bleve, SQLite variants) can be swapped
// without touching the rest of the stack.
type Backend interface {
	// BatchIndex inserts or replaces sessions. Implementations must be
	// idempotent: repeated calls with the same session ID overwrite.
	BatchIndex(ctx context.Context, sessions []cass.Session) error

	// Search executes a full-text query and returns ranked hits.
	Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error)

	// SessionCount returns the number of indexed sessions.
	SessionCount(ctx context.Context) (int, error)

	// Close releases resources held by the backend.
	Close() error
}

// BackendKind names the available backend implementations.
type BackendKind string

const (
	// BackendSQLite is the default pure-Go SQLite FTS5 backend.
	BackendSQLite BackendKind = "sqlite"

	// BackendSQLitePorter uses SQLite FTS5 with porter stemming.
	// Reduces unique token count ~30-50%, improving recall and shrinking
	// the posting-list index.
	BackendSQLitePorter BackendKind = "sqlite-porter"

	// BackendDuckDB uses DuckDB with its built-in FTS extension.
	// Requires CGO and the duckdb build tag.
	BackendDuckDB BackendKind = "duckdb"
)

// BackendConfig controls backend selection and tuning.
type BackendConfig struct {
	// Kind selects the backend. Defaults to BackendSQLite.
	Kind BackendKind

	// Path is the file path for persistent backends (SQLite, DuckDB).
	// Use ":memory:" for an in-memory database.
	Path string

	// MaxFTSBytes caps the amount of text sent to the FTS index per session.
	// Zero means use the backend's default (currently 32 KB for SQLite).
	MaxFTSBytes int
}

// openBackend constructs the appropriate Backend for the given config.
// DuckDB is only available when compiled with the duckdb build tag.
func openBackend(cfg BackendConfig) (Backend, error) {
	switch cfg.Kind {
	case BackendDuckDB:
		return openDuckDB(cfg)
	case BackendSQLitePorter:
		return openSQLitePorter(cfg)
	default:
		return openSQLite(cfg)
	}
}

// Stats holds size metrics collected from a backend for benchmarking.
type Stats struct {
	// TotalRows is the number of sessions in the main table.
	TotalRows int64

	// IndexSizeBytes is the on-disk size of the FTS index (best effort).
	IndexSizeBytes int64

	// StoreSizeBytes is the total on-disk size of all backend files.
	StoreSizeBytes int64
}

// Statter is an optional interface backends can implement to expose
// index size statistics for benchmarking.
type Statter interface {
	BackendStats(ctx context.Context) (Stats, error)
}

// sizeRatio returns the FTS index overhead as a multiplier over raw text.
// e.g. 27 MB text → 8 GB index ≈ ratio 300.
func sizeRatio(rawTextBytes, indexBytes int64) float64 {
	if rawTextBytes == 0 {
		return 0
	}
	return float64(indexBytes) / float64(rawTextBytes)
}

// dailyUsage is a sentinel to make sizeRatio visible to the test file.
var _ = sizeRatio

// DailyTokenRow and other supplementary types are defined in store.go
// so they stay close to the SQLite implementation that populates them.

// ensure time import is used
var _ = time.Now
