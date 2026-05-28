// Command cccat filters and displays Claude Code session entries.
//
// It reads JSONL session files from stdin or file arguments and writes
// matching entries to stdout. It works like grep but understands session
// structure: entries can be filtered by type, role, tool name, or a
// substring of their text content.
//
// # Usage
//
//	cccat [flags] [file...]
//
// # Flags
//
//	-type TYPE      Filter by entry type (user, assistant, system, summary, progress).
//	-role ROLE      Filter by message role (user, assistant, developer).
//	-tool NAME      Keep only tool_use blocks for the named tool.
//	-text           Print only text content, not JSON.
//	-tool-names     Print only the tool names used.
//	-format FMT     Output format: json, jsonl, text (default: auto).
//	-grep STR       Keep entries containing STR.
//	-c              Print count of matching entries instead of the entries.
//
// # Examples
//
//	cccat -role user session.jsonl
//	cccat -role assistant -tool-names session.jsonl
//	ccsessions -format jsonl | cccat -type user
package main
