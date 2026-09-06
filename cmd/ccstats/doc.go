// Command ccstats reports token usage, tool counts, and timing for sessions.
//
// It reads one or more coding-agent session files (Claude Code, Codex,
// opencode, pi) and prints aggregate statistics: message and tool-use counts,
// start/end times and duration, and input/output/cache token totals.
//
// ccstats characteristics reports cross-session usage characteristics over
// the same file set. It supports -unit, -verbose, -parallel-window,
// -context-threshold, -long-running-threshold, -pricing-file, and -redact
// for JSON output.
//
// ccstats weekday and ccstats repos report usage by day of week and by
// working directory, normalized the way the cass index normalizes it: each
// file's harness is named, so a harness that counts cached tokens inside its
// input count is not counted twice. Both honor -unit, -n, and -format.
//
// ccstats watch refreshes the characteristics report every 60 seconds.
//
// # Usage
//
//	ccstats [flags] [file...]
//	ccstats characteristics [flags] [file...]
//	ccstats weekday [flags] [file...]
//	ccstats repos [flags] [file...]
//
// # Flags
//
//	-since DUR    Scan sessions modified within the last DUR (e.g. 16h, 7d).
//	-format FMT   Output format: text (default) or json.
//	-n N          Rows to print in the repos report (default 20).
//
// # Examples
//
//	ccstats ~/.claude/projects/*/44fc759a*.jsonl
//	ccstats -since 16h -format json
//	ccstats weekday -since 12w
//	ccstats repos -since 7d -n 10
package main
