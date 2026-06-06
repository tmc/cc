// Package service orchestrates session collection, indexing, and search.
//
// A Service wraps a SQLite-backed [store.Store] and a set of
// [cass.Collector] implementations. It exposes higher-level operations
// (Detect, Index, Search, Stats, Links, Graph, ...) plus targeted
// ingestion of API request exports, Proxyman SessionV2 captures, team configs,
// jobs, and agent definitions.
//
// # Configuration
//
// Config fields:
//   - DBPath: path to the SQLite database. If empty, defaults to
//     ~/.cache/cass/index.db. Parent directories are created on New.
//   - Collectors: collector set to use. If nil, a default set covering
//     Claude Code, Gemini CLI, Codex, opencode, OpenClaw, Antigravity, and Cursor
//     is installed.
//   - Logger: slog logger used for non-fatal warnings. Defaults to
//     [slog.Default].
//
// # Concurrency
//
// Index fans out one goroutine per collector and serializes writes to
// the underlying store with an internal mutex. All read methods
// (Search, Session, Stats, Links, Graph, QueryRequests, ...) are safe
// to call concurrently with each other and with Index. Close must not
// race with other calls; the caller is responsible for quiescing the
// Service before closing.
//
// # Example
//
//	svc, err := service.New(service.Config{})
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer svc.Close()
//
//	if _, err := svc.Index(ctx, false); err != nil {
//		log.Fatal(err)
//	}
//	res, err := svc.Search(ctx, cass.SearchRequest{Query: "rebase"})
//	if err != nil {
//		log.Fatal(err)
//	}
//	for _, h := range res.Hits {
//		fmt.Println(h.SessionID, h.Snippet)
//	}
package service
