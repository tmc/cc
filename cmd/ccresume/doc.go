// Command ccresume finds a recent coding-agent session and resumes it.
//
// ccresume searches recent sessions for a query (positional argument, or the
// clipboard if -clip is set), groups results by git worktree, and prints the
// resume command for the best match (e.g. claude -r <id>, or the agent-specific
// form for codex, gemini, opencode, pi, and antigravity sessions). With -l the
// resume is launched directly. Sessions for the current worktree are prioritized.
//
// A claude.ai URL is accepted as the query and reduced to its identifier before
// searching, so pasting an artifact link (query string and all) resolves the
// session that created the artifact. The same holds for code/session and design
// links.
//
// An iTerm2 session ID is also accepted as the query (a pane UUID, with or
// without the wNtNpN: prefix that $ITERM_SESSION_ID carries). It resolves to the
// harness session most recently bound to that pane, using the per-pane binding
// events a SessionStart hook records under ~/.it2/sessions. When no binding
// exists, the query is searched as ordinary text.
//
// A copied it2 log line ([it2:send-text src=… dst=…], also send-key and other
// [it2:…] forms) or a collab-msg header (<collab-msg src=…>) is accepted too:
// the pane ID is extracted and resolved as above. By default the src pane (the
// sender) is used; -sid-side=dst selects the recipient instead.
//
// # Usage
//
//	ccresume [flags] [query...]
//
// # Flags
//
//	-l           Launch claude instead of printing the resume command.
//	-since dur   Only consider sessions modified within this duration (default 7d).
//	-1           Print only the single most recent match.
//	-clip        Use the clipboard contents as the query when no args given (default true).
//	-paths       Print raw session file paths instead of resume commands.
//	-has text    Only include sessions whose transcript contains text.
//	-cmd text    Only include sessions with a tool command containing text.
//	-result text Only include sessions with a tool result containing text.
//	-wrote path  Only include sessions that wrote a file matching path (or glob).
//	-dir path    Only include sessions recorded in this directory.
//	-harness h   Only include sessions from h (for example, codex).
//	-sid-side s  For an it2 send-text line query, resume the src (default) or dst pane.
//	-sort mode   Order matches by relevance (default) or recency.
//	-info            Annotate each resume line with session name, size, an occurrence sparkline, and running state (implies -show-is-running).
//	-info-line       Print the -info annotation on its own line under each resume command (implies -info).
//	-show-is-running Annotate each resume line whose session is already open in a running process.
//	-no-color        Disable ANSI color in -info annotations (also honors NO_COLOR).
//
// # Examples
//
// Resume the most recent session matching "kafka rebalance":
//
//	ccresume "kafka rebalance"
//
// Use the current clipboard contents as the query and launch immediately:
//
//	ccresume -l
//
// Limit the search window to the last 24 hours:
//
//	ccresume -since 24h "auth refactor"
//
// Find sessions that ran a command and captured a particular result:
//
//	ccresume -cmd "it2 session current" -result 052AB527
//
// Find the session that last edited a particular file, or any file matching a
// glob:
//
//	ccresume -wrote cmd/ccresume/main.go
//	ccresume -wrote '*_test.go'
//
// Resume the session that created an artifact, from its claude.ai URL:
//
//	ccresume -1 "https://claude.ai/code/artifact/00000010-1000-4000-8000-000000000002?org=..."
//
// Resume the session that ran in an iTerm2 pane, from its session ID:
//
//	ccresume "$ITERM_SESSION_ID"
//
// Resume a pane's session from a copied it2 log line (resolves the src pane):
//
//	ccresume "[it2:send-text src=A5B2B529 dst=481499F5]"
//
// Annotate each match with size and an occurrence sparkline to spot the
// substantive session without resuming each:
//
//	ccresume -info "kafka rebalance"
//
// Mark which matches are already open in a running process, so you attach to the
// live one rather than starting a second copy:
//
//	ccresume -show-is-running "kafka rebalance"
//
// The annotation is a compact trailing shell comment (# 482× 1.2kL 3@ 8%
// ▁▁▂▁▅█ 2h "session name" — messages, lines, hits, density, sparkline,
// last-active age, then the user-assigned name if any; ×/L/@ mark counts so
// the trailing age is never read as one, and the name trails the stats so the
// numeric columns stay aligned), so the line stays safe to pipe into sh and
// fits on one row. The comments are aligned into one column across the result set
// (capped, so one long path overruns rather than widening the column for
// everyone), and within them each field is padded to a shared width — counts and
// age right-aligned, sparkline and RUNNING left-aligned — so the listing reads
// as a table. With -info-line the comment moves to its own indented line under
// each command instead, which keeps long resume lines scannable. On a terminal
// the name renders as a reverse-video segment (the way Claude Code's own UI
// shows it), the density+sparkline in cyan, a sub-hour age in green, and the
// RUNNING marker in green; plain output quotes the name. The "N%" token is the share of
// the session's timestamped lines that mention the query (central vs
// incidental), and the sparkline reads left (oldest) to right (newest) within
// that one session: ▁▁▁▁▁▁▁█ means the query only matched at the end (recent
// work), █▁▁▁▁▁▁▁ an early mention since dropped. Both are shown only for content
// queries; a query that resolved to a specific session (a claude.ai URL or an
// it2 SID) shows size and age only. On a terminal the comment is dimmed; piped
// output stays byte-clean (honors NO_COLOR/-no-color).
//
// With -show-is-running (implied by -info) a "RUNNING" marker is appended at the very end of any
// line whose session is currently open, found by matching the session identifier
// (the last part of the resume command) against the running process table; when
// several processes share the session it reads "# RUNNING ×N". It composes with
// -info — the size/occurrence detail comes first, the RUNNING marker sits last so
// it is easy to scan for down the right edge. A fresh session started without an
// identifier cannot be attributed to a match and is never marked.
//
// The -wrote filter keeps sessions that wrote a file matching its argument. A
// pattern with glob metacharacters (*?[) is matched with path matching against
// the full path and the basename ('*_test.go' matches by basename, 'cmd/*/x.go'
// by the full path); any other pattern is a case-insensitive substring, like the
// other content filters. Written paths are read from the normalized transcript
// across harnesses: Write/Edit/MultiEdit/NotebookEdit (Claude, opencode) and
// write_to_file/replace_file_content (Antigravity) carry the path in a tool
// input; codex apply_patch carries it in the patch body; and shell writes
// (redirects, tee, cp/mv, sed -i, touch, heredocs) are parsed from Bash
// commands. Reads never count. Coverage is best-effort for codex, whose writes
// are recovered heuristically from command text rather than a structured field.
package main
