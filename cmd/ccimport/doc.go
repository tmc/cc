// Command ccimport converts Claude Code session JSONL into Gemini CLI chat format.
//
// It reads a Claude Code session file and writes a Gemini session-*.json
// suitable for dropping into ~/.gemini/tmp/<project>/chats/, enabling
// handoff of a conversation from Claude Code to Gemini CLI.
//
// Usage:
//
//	ccimport -from SESSION.jsonl -to-project PATH [-out FILE] [-dry-run]
//
// If -out is omitted, the target path is derived from the Gemini
// project layout for -to-project.
package main
