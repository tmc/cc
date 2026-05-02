// Command cchistory searches Claude Code session NDJSON files.
//
// cchistory walks one or more session directories, scans `.ndjson`
// session files modified within a recent window, and prints messages
// matching an optional regex.
//
// # Usage
//
//	cchistory [flags] [N] [pattern]
//
// If the first positional argument parses as an integer it is treated
// as a value for -n. The remaining positional arguments are joined and
// compiled as a Go regular expression. With no pattern, recent
// messages are listed unfiltered.
//
// # Flags
//
//	-n N             Cap output to the last N matches (default 100).
//	-since DUR       Only consider files modified within DUR
//	                 (default 7d; supports d, w, plus stdlib durations).
//	-local           Search only the current directory.
//	-global          Search every standard sessions directory.
//	-session ID      Match only files whose path contains ID.
//	-sessions DIR    Search this directory instead of auto-discovery.
//	-commands        Limit matches to user messages.
//	-responses       Limit matches to assistant messages.
//	-tool-use        Limit matches to tool_use entries.
//	-i               Case-insensitive pattern matching.
//	-context N       Show N messages of context around each match.
//	-A N, -B N       Show N messages after / before each match.
//	-files           Print only matching file paths.
//	-count           Print "file:N" match counts per file.
//	-format FMT      Output format: text (default), json, compact.
//	-no-filename     Suppress filename prefixes in text/compact output.
//
// # Session Discovery
//
// Without -sessions, -local, or -global, cchistory searches the
// current directory plus, when in a git worktree:
//
//	<worktree>/.sessions
//	<worktree>/.claude/sessions
//	~/.claude/sessions/<git-hash>
//	~/.claude/sessions
//
// -global adds `.`, `.sessions`, `~/.claude/sessions`, and
// `~/.config/claude/sessions`. -local searches only `.`.
//
// # Examples
//
// Show the last 50 messages for the current project:
//
//	cchistory 50
//
// Search assistant messages for "retry" in the last day:
//
//	cchistory -responses -since 24h retry
//
// List session files containing the word "kafka":
//
//	cchistory -files kafka
//
// Emit machine-readable JSON for further processing:
//
//	cchistory -format json -since 7d "error|panic"
package main
