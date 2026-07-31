// Command ccloc prints a coding-agent harness's data directories.
//
// Each harness keeps its data under a home directory (~/.claude, ~/.gemini,
// ~/.codex, ...) with a sessions tree below it. Claude Code, Gemini CLI, and
// pi additionally keep a per-project directory derived from the project path:
// Claude and Gemini encode "/Volumes/example/go" as
// projects/-Volumes-example-go, pi as sessions/--Volumes-example-go--. ccloc
// resolves these locations so scripts and other programs need not hardcode
// the layouts.
//
// # Usage
//
//	ccloc [flags] [DIR]
//
// By default ccloc prints the harness's per-project directory for DIR
// (default: the current directory; made absolute before encoding). Codex,
// opencode, and antigravity organize sessions by date or ID rather than by
// project, so the default form is an error for them; use -root or -home.
//
// # Flags
//
//	-harness h   Harness to resolve for (claude, gemini, codex, opencode,
//	             pi, antigravity). Default claude.
//	-claude, -gemini, -codex, -opencode, -pi, -antigravity
//	             Shorthand for -harness <name>.
//	-home        Print the harness data home.
//	-root        Print the harness sessions root.
//	-check       Exit 0 if the directory exists, 1 otherwise.
//	-create      Create the directory if it does not exist.
//	-sessions    Print the `sessions` subdirectory of the project directory.
//	-short       Replace the leading $HOME with `~`.
//
// # Examples
//
// Print the Claude Code project directory for the current directory:
//
//	ccloc
//
// Print pi's session directory for a project:
//
//	ccloc -pi ~/work/proj
//
// Print the codex sessions root, and every harness home:
//
//	ccloc -codex -root
//	for h in claude gemini codex opencode pi antigravity; do ccloc -harness $h -home; done
//
// Check whether a path has a Claude cache and create one if not:
//
//	ccloc -check ~/work || ccloc -create ~/work
package main
