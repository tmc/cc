// Command ccbrowse browses coding-agent sessions in a TUI without resuming them.
//
// ccbrowse lists recent Claude Code, Codex, Gemini, opencode, and pi sessions
// in a filterable picker, renders any session's transcript read-only in a
// scrollable viewport, and never launches an agent. Selecting a session with
// Enter in the list opens its transcript; pressing y (in either view) quits
// and prints the shell command that would resume the session, so resuming
// stays an explicit, separate step.
//
// # Usage
//
//	ccbrowse [flags]
//
// # Flags
//
//	-since DUR     Only show sessions modified within DUR
//	               (default 14d; supports d, w, plus stdlib durations).
//	-all           Show sessions from all projects, not just the
//	               current directory's project.
//	-n N           Cap the list to N sessions (default 200).
//	-max N         Render at most the last N entries of a transcript
//	               (default 2000; 0 means no limit).
//
// # Keys
//
//	List view:
//	  type            Filter sessions (matches ID, project, title, prompt).
//	  ↑/↓, ctrl+p/n   Move selection.
//	  enter           Open the transcript (read-only).
//	  y               Quit and print the resume command for the selection.
//	  esc             Clear the filter, then quit.
//	  ctrl+c          Quit.
//
//	Transcript view:
//	  ↑/↓, pgup/pgdn  Scroll.
//	  g/G             Jump to top / bottom.
//	  t               Toggle per-entry timestamps.
//	  y               Quit and print the resume command.
//	  esc, q          Back to the list.
//
// # Output
//
// ccbrowse writes nothing to stdout except the resume command printed by y,
// so `$(ccbrowse ...)` and clipboard pipes stay clean. Example:
//
//	cd ~/src/proj; claude -r 00000011-1000-4000-8000-000000000002
//
// For non-interactive listing or rendering, see ccsessions and ccfmt.
package main
