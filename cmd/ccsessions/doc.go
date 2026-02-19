// Command ccsessions lists and summarizes Claude Code sessions.
//
// It reads JSONL session files from ~/.claude/projects/ and displays
// session metadata including timestamps, project, model, and a summary
// of the conversation.
//
// Usage:
//
//	ccsessions [flags]
//
// Flags:
//
//	-since duration    Only show sessions modified within duration (default "16h")
//	-project string    Filter by project name substring
//	-format string     Output format: text, json, jsonl (default "text")
//	-n int             Max sessions to show (default 50)
//	-v                 Verbose: show first user message
package main
