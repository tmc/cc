// Command ccrender renders a single coding-agent session in the pager.
//
// ccrender resolves its argument to a session file — a path, or a session ID
// (or unambiguous ID prefix) looked up across the Claude Code, Codex, Gemini,
// opencode, and pi session stores — and renders the transcript read-only. It
// never resumes the session.
//
// On a terminal the transcript goes through the pager, the way git does:
// $CCRENDER_PAGER, else $PAGER, else less, with LESS defaulting to FRX when the
// user has not set it. Navigation therefore comes from the pager — search with
// /, repeat with n, jump with G — rather than from a viewport reimplementation
// of it. Set PAGER to the empty string (or pipe stdout) to get the text with no
// pager at all.
//
// The viewport owns the one thing a pager cannot do, following a live session:
// -follow uses it, and -tui selects it for a finished one. Document formats
// belong to ccfmt: passing -format delegates there, keeping batch formatting in
// one place.
//
// # Usage
//
//	ccrender [flags] SESSION
//	ccrender [flags] path/to/session.jsonl
//
// # Flags
//
//	-format FMT    Delegate to ccfmt with this format (text, transcript,
//	               markdown/md, json, jsonl) instead of opening the TUI.
//	-follow        Tail the session file for new entries (TUI only).
//	-tui           Use the built-in viewport instead of the pager.
//	-timestamps    Start with per-entry timestamps shown.
//	-expand        Start with folded output expanded (see ctrl+o).
//	-max N         Render at most the last N entries (default 2000;
//	               0 means no limit).
//	-since DUR     How far back to search when resolving an ID
//	               (default 30d).
//
// # Keys
//
// These are the built-in viewport's keys (-follow or -tui); under the pager,
// its own keys apply.
//
//	↑/↓, pgup/pgdn  Scroll.
//	g/G             Jump to top / bottom.
//	ctrl+o          Toggle expanded output: every tool-result line, no
//	                hidden bodies, no collapsed Explored groups.
//	t               Toggle per-entry timestamps.
//	f               Toggle follow mode.
//	q, esc, ctrl+c  Quit.
//
// # Examples
//
// View a session by ID prefix:
//
//	ccrender 00000011
//
// Watch a live session:
//
//	ccrender -follow 763ce2cc
//
// Read a session in the pager, or pipe it, or export a document via ccfmt:
//
//	ccrender 00000011
//	ccrender 00000011 | grep -n panic
//	ccrender -format md 00000011 > transcript.md
package main
