// Command ccfiles extracts file operations from Claude Code sessions.
//
// It reports every file that was written, edited, read, or otherwise
// touched during a session, including files modified by Bash commands
// (redirects, tee, cp, mv).
//
// # Usage
//
//	ccfiles [flags] [file...]
//
// # Flags
//
//	-writes       Show only write operations (Write, Edit, Bash writes).
//	-reads        Show only read operations.
//	-unique       Deduplicate and print only file paths.
//	-since DUR    Scan sessions modified within the last DUR (e.g. 16h).
//	-format FMT   Output format: text (default), json, jsonl.
//	-count        Print file counts sorted by frequency.
//
// # Examples
//
//	ccfiles session.jsonl
//	ccfiles -writes -unique -since 16h
package main
