// Command ccresume finds a recent Claude Code session and resumes it.
//
// ccresume searches recent sessions for a query (positional argument, or the
// clipboard if -clip is set), groups results by git worktree, and prints the
// `claude resume` command for the best match. With -l the resume is launched
// directly. Sessions for the current worktree are prioritized.
//
// # Usage
//
//	ccresume [flags] [query...]
//
// # Flags
//
//	-l           Launch claude instead of printing the resume command.
//	-since dur   Only consider sessions modified within this duration (default 7d).
//	-1           Print only the single most recent match.
//	-clip        Use the clipboard contents as the query when no args given (default true).
//
// # Examples
//
// Resume the most recent session matching "kafka rebalance":
//
//	ccresume "kafka rebalance"
//
// Use the current clipboard contents as the query and launch immediately:
//
//	ccresume -l
//
// Limit the search window to the last 24 hours:
//
//	ccresume -since 24h "auth refactor"
package main
