// Command cctool extracts tool use details from Claude Code sessions.
//
// It prints what tools were invoked and with what arguments, so bash
// commands, file edits, searches, and other tool calls can be reviewed
// outside the original session UI.
//
// # Usage
//
//	cctool [flags] [file...]
//
// # Flags
//
//	-name NAME    Filter by tool name (e.g. Bash, Edit, Read, Write, Grep, Glob).
//	-names        List distinct tool names with counts.
//	-show-input   Show full tool input JSON.
//	-compact      One-line output per tool use.
//
// # Examples
//
//	cctool -name Bash session.jsonl
//	cctool -name Edit -show-input session.jsonl
//	cctool -names session.jsonl
package main
