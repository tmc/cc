// Command cctime shows a timeline of events in a coding-agent session.
//
// It prints a chronological view of user messages, assistant actions,
// tool uses, and errors with timestamps and inter-event gaps.
//
// # Usage
//
//	cctime [flags] [file...]
//
// # Flags
//
//	-tools        Show tool uses inline with messages.
//	-brief        One line per entry, minimal detail.
//	-since DUR    Scan sessions modified within the last DUR (e.g. 16h).
//
// # Examples
//
//	cctime session.jsonl
//	cctime -tools session.jsonl
package main
