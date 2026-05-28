// Command ccerr finds errors, failures, and retries in Claude Code sessions.
//
// It scans session JSONL for tool errors, failed bash commands, API
// errors, user rejections, and interruptions, and prints one record per
// occurrence.
//
// # Usage
//
//	ccerr [flags] [file...]
//
// # Flags
//
//	-since DUR    Scan sessions modified within the last DUR (e.g. 16h).
//	-v            Show full error content, not a truncated summary.
//	-c            Print the count of matches only.
//	-format FMT   Output format: text (default) or json.
//
// # Examples
//
//	ccerr session.jsonl
//	ccerr -since 16h -format json
package main
