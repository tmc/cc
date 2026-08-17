// Command cchooks reports the hook configuration of coding agents.
//
// Hooks are commands an agent runs at points in its lifecycle. They come from
// several files per agent — user settings, project settings, plugins, system
// policy — and a handler can be present, well-formed, and never run. cchooks
// reads every layer and prints one row per handler with its provenance, then
// the problems it can see without running anything.
//
// cchooks never writes. Installing hooks is a separate, and riskier, job.
//
// # Usage
//
//	cchooks [status] [flags]
//
// # Flags
//
//	-agent NAME   Only report this agent (claude-code, codex).
//	-dir DIRS     Comma-separated directories whose .claude/settings.json
//	              to include (default: the working directory).
//	-json         Emit handlers and diagnostics as JSON.
//	-v            Include informational diagnostics.
//
// # Exit status
//
//	0  healthy
//	1  degraded: warnings only
//	2  broken: at least one error
//	3  cchooks itself failed
//
// An agent that is not installed contributes nothing and is not a fault, so a
// machine running only one agent still exits 0.
//
// # Diagnostics
//
// The codes are stable and reported in the -json output:
//
//	unmatchable-matcher     A matcher written in permission-rule syntax, e.g.
//	                        Bash(cmd*). Matchers are compiled as regular
//	                        expressions and tested against the tool name, so
//	                        this never matches. The condition belongs in "if".
//	misplaced-if            An "if" written on the matcher group rather than
//	                        the handler. Nothing reads it there, so the
//	                        handler runs on every call the matcher admits.
//	                        Where an unmatchable matcher fails closed, this
//	                        fails open.
//	missing-hooks-wrapper   A Codex hooks.json whose event names sit at the
//	                        file root. Codex requires a "hooks" object and
//	                        rejects unknown root keys, dropping every hook
//	                        with only a warning.
//	parked                  A disabled-by-rename block such as "xhooks".
//	command-substitution    $(...) in a handler, where a resolution failure
//	                        becomes an empty path segment.
//	per-event-subprocess    A per-event handler shelling out to a terminal
//	                        command; resolve identity once at session start.
//	duplicate-handler       The same handler in two layers; it runs twice.
//	missing-script          A handler pointing at a script that is not there.
//	plugin-unresolved       An enabled plugin whose recorded installation is
//	                        gone. The agent refuses to load it, so none of its
//	                        hooks run even when a readable copy of the same
//	                        plugin remains elsewhere on disk.
//
// Handlers that cannot run are still listed, marked PARKED (inside a
// disabled-by-rename block) or DEAD (belonging to an uninstalled plugin), so
// that what a dormant hook would have done stays visible.
//
// # Examples
//
// Report everything, including informational findings:
//
//	cchooks status -v
//
// Check one agent from a script:
//
//	cchooks status -agent claude-code -json
package main
