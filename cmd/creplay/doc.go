// Command creplay replays a coding-agent session NDJSON file in a TUI.
//
// creplay loads the messages from a session file and animates them in
// a Bubble Tea viewport. With -follow it tails the file for new
// messages.
//
// # Usage
//
//	creplay SESSION_ID [flags]
//	creplay -file PATH  [flags]
//
// When SESSION_ID is given (no -file), creplay searches `.`,
// `.sessions`, and `~/.cc/sessions` for `session-<id>.ndjson`,
// `<id>.ndjson`, or `*<id>*.ndjson`.
//
// # Flags
//
//	-file PATH      Read messages from PATH instead of resolving an ID.
//	-follow         Tail the file for new messages (poll-based).
//	-speed FLOAT    Playback speed multiplier (default 1.0).
//
// # Keys
//
//	q, Ctrl+C       Quit.
//	space           Pause / resume playback.
//	f               Toggle follow mode.
//	r               Toggle raw NDJSON view.
//	?               Toggle help.
//	↑/↓, j/k        Scroll one line.
//	pgup/pgdn       Scroll one page.
//	Ctrl+u, Ctrl+d  Scroll a half page.
//
// # Examples
//
// Animate a recorded session at 2x:
//
//	creplay -speed 2 -file session-2026-04-01.ndjson
//
// Tail an active session:
//
//	SID=$(mksid)
//	echo "hello" | cmsg | tee session-$SID.ndjson
//	creplay -follow $SID
package main
