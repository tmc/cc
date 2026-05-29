// Command ccdiff shows file changes made during coding-agent sessions.
//
// It reconstructs unified diffs from Edit tool uses (old_string to
// new_string) and, with -writes, includes content produced by Write
// tool uses. Input is one or more session JSONL files, or stdin.
//
// # Usage
//
//	ccdiff [flags] [file...]
//
// # Flags
//
//	-path STR    Only include operations whose file path contains STR.
//	-since DUR   Scan sessions modified within the last DUR (e.g. 16h).
//	-stat        Print a diffstat summary instead of full diffs.
//	-writes      Include content of Write tool uses.
//
// # Examples
//
//	ccdiff session.jsonl
//	ccdiff -path models/qwen2.go session.jsonl
//	ccdiff -since 16h -stat
package main
