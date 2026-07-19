// Package it2bind resolves the mapping between iTerm2 session IDs and
// coding-agent (harness) session IDs.
//
// The two identifiers live in independent namespaces with no stored join: an
// iTerm2 session ID names a terminal pane, while a harness session ID names a
// transcript (the .jsonl filename). Neither Claude Code nor codex records the
// other's ID at session start, so historically the mapping was only
// recoverable when a session happened to run an it2 command that echoed its
// own pane ID into its transcript.
//
// A SessionStart hook closes the gap. It fires on both startup and resume with
// the harness session_id on stdin and $ITERM_SESSION_ID in its environment, so
// a small writer can append a binding event into a per-pane directory:
//
//	~/.it2/sessions/<it2_sid>/artifacts/claude-code-events.ndjson
//
// Because a harness session may be resumed in a different pane (a new window or
// a different machine), the mapping is one harness session to many iTerm2
// session IDs, ordered over time — a sequence of bindings, not a 1:1 key.
// Bindings are grouped by (session_id, it2_sid) into time intervals: a binding
// ends only when the pane's session changes or the session moves to a different
// pane. iTerm2 restores a pane across a reboot keeping its session ID, so a
// binding can span multiple boots; each event still carries a boot_id
// (kern.bootsessionuuid), retained as interval metadata but not a boundary.
//
// This package reads and canonicalizes the per-pane directories and folds their
// binding events into [Interval] records. The on-disk directory layout and
// canonicalization are stable; the per-line event schema is defined by [Event].
package it2bind
