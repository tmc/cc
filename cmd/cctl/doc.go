// Command cctl dispatches to the other cc utilities.
//
// cctl looks up its first argument as a subcommand name (or short alias),
// resolves the corresponding binary on PATH (falling back to the directory
// of the cctl executable), and execs it with the remaining arguments.
// stdin, stdout, and stderr are forwarded; the subcommand's exit code is
// propagated.
//
// # Usage
//
//	cctl <subcommand> [options] [args]
//
// # Subcommands
//
//	cass         → cass        Show the cass CLI.
//	msg,     m   → cmsg        Format stdin as Claude messages.
//	replay,  r   → creplay     Replay sessions in a terminal UI.
//	sid,     s   → mksid       Generate session IDs.
//	history, h   → cchistory   Search session history.
//	config,  c   → ccconfig    Manage configuration.
//	loc,     l   → ccloc       Show agent cache location.
//	sessions,ss  → ccsessions  List recent sessions.
//	cat          → cccat       Filter and display session entries.
//	stats        → ccstats     Report token usage and tool counts.
//	tool         → cctool      Extract tool-use details.
//	files        → ccfiles     Extract file operations.
//	diff         → ccdiff      Show file changes from sessions.
//	time         → cctime      Show event timeline.
//	err          → ccerr       Find errors and retries.
//	team,    t   → ccteam      Manage agent teams.
//	spawn,   sp  → ccspawn     Spawn agents in teammate mode.
//	inbox,   in  → ccinbox     Read and write inbox messages.
//	task,    tk  → cctask      Manage persistent tasks.
//	agent,   ag  → ccagent     Inspect agent status.
//	approve, ap  → ccapprove   Handle approval workflows.
//	handoff, ho  → cchandoff   Build cross-tool handoff prompts.
//	memory,  mm  → ccmemory    List and read auto-memory files.
//	goals        → cass goals   Show goal-mode objectives indexed by cass.
//	skills       → cass skills  Show skill usage indexed by cass.
//	requests     → cass requests Show indexed API request breakdown.
//	web          → cass web     Start the cass web UI server.
//
//	help [cmd]   Show usage for cctl or a subcommand.
//	version      Show cctl version and resolved subcommand paths.
//
// # Resolution
//
// For each subcommand invocation cctl tries, in order:
//
//  1. exec.LookPath(binary)
//  2. <directory of cctl>/<binary>
//
// If neither is found, cctl prints a "go install" hint and exits with
// status 2.
//
// # Examples
//
//	echo "hello" | cctl msg
//	cctl sid
//	cctl history -since 24h "error"
//	cctl handoff -from session.jsonl -to gemini
//
// # Exit Codes
//
//	0   Success.
//	1   cctl could not exec the subcommand.
//	2   Unknown subcommand or subcommand binary not found.
//	N   Any other code is propagated from the subcommand.
package main
