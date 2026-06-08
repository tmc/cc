// Command ccresume finds a recent coding-agent session and resumes it.
//
// ccresume searches recent sessions for a query (positional argument, or the
// clipboard if -clip is set), groups results by git worktree, and prints the
// resume command for the best match (e.g. claude -r <id>, or the agent-specific
// form for codex, gemini, opencode, and pi sessions). With -l the resume is
// launched directly. Sessions for the current worktree are prioritized.
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
//	-paths       Print raw session file paths instead of resume commands.
//	-has text    Only include sessions whose transcript contains text.
//	-cmd text    Only include sessions with a tool command containing text.
//	-result text Only include sessions with a tool result containing text.
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
//
// Find sessions that ran a command and captured a particular result:
//
//	ccresume -cmd "it2 session current" -result 052AB527
package main
