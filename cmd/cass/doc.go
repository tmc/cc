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
//	goals      List goal-mode objectives.
//	skills     List skill usage.
//	links      Show inter-session communication graph.
//	map        Show iTerm2 <-> Claude session ID mappings.
//	stats      Show index statistics.
//	subagents  List Task subagent runs.
//	workflows  List native workflow runs.
//	requests   Show indexed API request breakdown.
//	web        Start the web UI server.
//
// # Common flags
//
//	-db path    Path to the SQLite index (default ~/.cache/cass/index.db).
//	--api-request-dir  Directory of API request export files to ingest.
//	-json       Emit machine-readable JSON instead of text.
//	-v          Verbose logging.
//
// # Search flags
//
// These apply to both search and resume:
//
//	-snippet N  Tokens of surrounding text in each match snippet, 0 to 64
//	            (default 40). FTS5 documents 64 tokens as the maximum and
//	            returns one snippet per row, so that is the ceiling. 0
//	            omits the snippet line.
//	-render     Open the top hit's transcript in ccrender, positioned at
//	            the first match of the query.
//
// # Examples
//
// Detect agents and index their sessions:
//
//	cass detect
//	cass index
//	cass index ~/.codex/sessions/2026/05/03/session.jsonl
//
// Search the index, optionally filtered by workspace or git worktree:
//
//	cass search "retry policy"
//	cass search --git-common-dir /path/to/repo "retry policy"
//	cass search -snippet 64 "retry policy"
//	cass search -render "retry policy"
//
// List subagent runs for a session:
//
//	cass subagents --by-agent-type
//
// See the [github.com/tmc/cc/cass] package for the underlying library and
// data model.
package main
