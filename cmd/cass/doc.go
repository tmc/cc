// Command cass indexes and searches AI coding-agent session history.
//
// CASS (Coding Agent Session Search) aggregates sessions from Claude Code,
// Codex, Cursor, and other agents into a unified SQLite FTS5 index, then
// exposes search, resumption, and graph views over the resulting corpus.
//
// # Usage
//
//	cass <command> [args]
//
// # Commands
//
//	detect     Detect installed AI coding agents.
//	index      Index sessions from detected agents.
//	search     Search indexed sessions.
//	resume     Search and resume a session interactively.
//	links      Show inter-session communication graph.
//	map        Show iTerm2 <-> Claude session ID mappings.
//	stats      Show index statistics.
//	subagents  List Task subagent runs.
//	requests   Show HAR-derived API request breakdown.
//	web        Start the web UI server.
//
// # Common flags
//
//	-db path    Path to the SQLite index (default ~/.cache/cass/index.db).
//	-json       Emit machine-readable JSON instead of text.
//	-v          Verbose logging.
//
// # Examples
//
// Detect agents and index their sessions:
//
//	cass detect
//	cass index
//
// Search the index, optionally filtered by workspace or git worktree:
//
//	cass search "retry policy"
//	cass search --git-common-dir /path/to/repo "retry policy"
//
// List subagent runs for a session:
//
//	cass subagents --by-agent-type
//
// See the [github.com/tmc/cc/cass] package for the underlying library and
// data model.
package main
