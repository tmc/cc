//go:build !duckdb

// When compiled without the duckdb build tag, openDuckDB returns an error
// instructing the caller to rebuild with -tags duckdb.
package store

import "fmt"

func openDuckDB(cfg BackendConfig) (Backend, error) {
	return nil, fmt.Errorf("duckdb backend requires CGO; rebuild with -tags duckdb")
}
