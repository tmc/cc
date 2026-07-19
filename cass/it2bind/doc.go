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
// Because a harness session may be resumed in a different pane (a new window, a
// reboot, a different machine), the mapping is one harness session to many
// iTerm2 session IDs, ordered over time — a sequence of bindings, not a 1:1
// key. iTerm2 session IDs are only comparable within a single boot, so each
// binding carries a boot_id (kern.bootsessionuuid); bindings are grouped by
// (session_id, boot_id) into time intervals.
//
// This package reads and canonicalizes the per-pane directories and folds their
// binding events into [Interval] records. The on-disk directory layout and
// canonicalization are stable; the per-line event schema is defined by [Event].
package it2bind
