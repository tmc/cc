// Package cass implements Coding Agent Session Search.
//
// CASS aggregates coding session history from AI agents (Claude Code, Codex,
// Cursor, etc.) into a unified, searchable index backed by SQLite FTS5.
//
// # Architecture
//
// The system is composed of three layers:
//
//  1. Collection ([cass/collector] package):
//     Discovers and parses session logs from various agents.
//     Each agent has a dedicated [Collector] implementation.
//
//  2. Storage ([cass/store] package):
//     Stores session data in SQLite and serves full-text search queries
//     via FTS5 with BM25 ranking.
//
//  3. Service ([cass/service] package):
//     Orchestrates collection, indexing, and search.
//
// # Usage
//
//	svc, err := service.New(service.Config{DBPath: "~/.cache/cass/index.db"})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer svc.Close()
//
//	// Index sessions from all detected agents.
//	n, err := svc.Index(ctx)
//
//	// Search across all indexed sessions.
//	results, err := svc.Search(ctx, cass.SearchRequest{Query: "auth bug"})
package cass
