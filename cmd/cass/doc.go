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
//	skills     Look up which sessions used a skill.
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
//	-render     Open the top hit's transcript in the pager, positioned at
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
// Report token utilization across every indexed harness:
//
//	cass usage weekday
//	cass usage repos -n 8
//	cass usage calendar -since 52w
//	cass usage daily -bucket week -cumulative -by agent
//	cass usage sessions -since 1w -n 10
//	cass usage -check stats-cache
//
// # Usage
//
// The usage command reports one row per model request, with the four token
// counts normalized to the Anthropic convention so that harnesses which
// report cached tokens inside their input count are comparable with those
// which report them separately. Views:
//
//	weekday    Mean, median and active days per weekday over whole weeks.
//	heatmap    Weekday by local hour.
//	calendar   Week columns by weekday rows, as /stats and /usage daily draw.
//	repos      By repository, with the unattributed rows as a visible bucket.
//	providers  By harness and vendor.
//	models     Share and four-way split per model.
//	daily      A bar per day or week, optionally cumulative or split.
//	sessions   Each session's share of the window, largest first.
//
// Common flags: -since (Nw, Nd, or 0 for all time), -until, -unit
// (total, context, output, input, cost), -agent, -repo, -model, -tz,
// -no-sidechain, -n, -week-start, -session, and -format json.
//
// The sessions view divides by the whole window, so -session narrows which
// rows print without changing the denominator: it answers what fraction of
// the week's tokens one session accounted for.
//
// Rows appear only for sessions indexed since the usage table was added;
// cass has no backfill, so run cass index --force once and the command
// warns while the index predates the table.
//
// # Skills
//
// The skills command answers which sessions, in any harness, used a skill.
// With no argument it lists skills by the number of sessions that used them;
// with a skill it lists those sessions, most recent first, and how each one
// used it:
//
//	cass skills
//	cass skills go-team-codebase-review
//	cass skills -agent codex -since 168h 'go-team-*'
//	cass skills -summary -pwd
//	cass search -skill nlm "notebook"
//
// A skill matches by name without regard to case or plugin prefix, so
// deep-research matches anthropic-skills:deep-research. It may be a glob,
// and an argument containing a slash matches the skill's path instead.
//
// A session uses a skill when it selects it (a Skill tool call, a slash
// command, a Codex $skill mention, Pi's /skill:name, OpenCode's skill tool,
// Gemini's activate_skill) or loads its SKILL.md. Skills that were only
// listed as available, or only appeared in context, are kinds available and
// expanded; -kind all includes them.
//
// Skill signals are re-extracted when a session is indexed. To apply a newer
// extractor to sessions that have not changed, without rebuilding the
// index, run:
//
//	cass index -skills
//
// List subagent runs for a session:
//
//	cass subagents --by-agent-type
//
// See the [github.com/tmc/cc/cass] package for the underlying library and
// data model.
package main
