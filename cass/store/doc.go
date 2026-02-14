// Package store provides SQLite-backed session storage with FTS5 full-text search.
//
// The store uses modernc.org/sqlite (pure Go, no CGO) with FTS5 virtual tables
// for full-text search and BM25 ranking.
//
// # Schema
//
// Sessions are stored in a sessions table with an associated FTS5 virtual table
// for full-text search across titles and message content.
//
//	sessions     - Core session metadata and content
//	session_fts  - FTS5 virtual table for search
//	metadata     - Key-value store for index state (last scan time, schema version)
package store
