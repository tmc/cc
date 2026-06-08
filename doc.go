// Package cc reads AI coding-agent session files: Claude Code, Codex, opencode,
// and pi.
//
// A session file is newline-delimited JSON (NDJSON): each line is one [Entry].
// [Reader] streams entries from an io.Reader, decoding both the Claude JSONL
// format and the Codex envelope format transparently. [ReadFile] additionally
// detects opencode and pi sessions by path and normalizes them into the same
// [Entry] shape. [ReadFile] and [ReadAll] are convenience wrappers, and
// [Summarize] folds a slice of entries into a [SessionSummary].
//
//	entries, err := cc.ReadFile(ctx, path)
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(cc.Summarize(path, entries).FirstPrompt)
//
// # Streaming
//
// [Reader] reads incrementally and checks its context during [Reader.Next], so
// callers can process or cancel arbitrarily large sessions without buffering
// the whole file. The line buffer grows to [MaxLineSize] to accommodate large
// tool-result payloads.
//
// # Related packages
//
// Concerns beyond session reading live in sibling packages:
//
//   - [github.com/tmc/cc/ccpaths]: agent home dirs and path encoding
//   - [github.com/tmc/cc/ccgit]: git worktree context resolution
//   - [github.com/tmc/cc/ccfs]: atomic-write and flock helpers
//   - [github.com/tmc/cc/ccteamcfg]: team configuration storage
//   - [github.com/tmc/cc/ccinboxstore]: per-agent inbox message files
//   - [github.com/tmc/cc/cctaskstore]: the team task store
//   - [github.com/tmc/cc/ccjobstore]: background job state and timelines
//   - [github.com/tmc/cc/ccagentdef]: user-defined agent templates
//
// The cmd directory holds the command-line tools built on these packages,
// including mksid (session IDs), cmsg (message formatting), and creplay
// (session replay).
package cc
