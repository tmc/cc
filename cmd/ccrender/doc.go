// Command ccrender renders a single coding-agent session, defaulting to a TUI.
//
// ccrender resolves its argument to a session file — a path, or a session ID
// (or unambiguous ID prefix) looked up across the Claude Code, Codex, Gemini,
// opencode, and pi session stores — and renders the transcript read-only in a
// scrollable viewport. It never resumes the session.
//
// For non-interactive output, ccrender delegates to ccfmt: pass -format, or
// pipe stdout (a non-terminal stdout implies -format text). This keeps batch
// formatting in one place; ccrender itself only owns the interactive view.
//
// # Usage
//
//	ccrender [flags] SESSION
//	ccrender [flags] path/to/session.jsonl
//
// # Flags
//
//	-format FMT    Delegate to ccfmt with this format (text, markdown/md,
//	               json, jsonl) instead of opening the TUI.
//	-follow        Tail the session file for new entries (TUI only).
//	-timestamps    Start with per-entry timestamps shown.
//	-max N         Render at most the last N entries (default 2000;
//	               0 means no limit).
//	-since DUR     How far back to search when resolving an ID
//	               (default 30d).
//
// # Keys
//
//	↑/↓, pgup/pgdn  Scroll.
//	g/G             Jump to top / bottom.
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
// Pipe a rendered transcript (delegates to ccfmt):
//
//	ccrender 00000011 | less
//	ccrender -format md 00000011 > transcript.md
package main
