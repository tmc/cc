// Command ccstats reports token usage, tool counts, and timing for sessions.
//
// It reads one or more Claude Code session JSONL files and prints
// aggregate statistics: message and tool-use counts, start/end times
// and duration, and input/output/cache token totals.
//
// ccstats characteristics reports cross-session usage characteristics over
// the same file set. It supports -unit, -verbose, -parallel-window,
// -context-threshold, -long-running-threshold, -pricing-file, and -redact
// for JSON output.
//
// # Usage
//
//	ccstats [flags] [file...]
//	ccstats characteristics [flags] [file...]
//
// # Flags
//
//	-since DUR    Scan sessions modified within the last DUR (e.g. 16h, 7d).
//	-format FMT   Output format: text (default) or json.
//
// # Examples
//
//	ccstats ~/.claude/projects/*/44fc759a*.jsonl
//	ccstats -since 16h -format json
package main
