// Command ccsessions lists recent coding-agent sessions.
//
// ccsessions reads session files from the Claude Code, Gemini CLI, Codex CLI,
// opencode, and pi session directories and prints one row per session with
// timestamp, ID, project, message counts, and (with -v) the first user
// prompt.
//
// # Usage
//
//	ccsessions [flags]
//
// # Flags
//
//	-since DUR     Only show sessions modified within DUR
//	               (default 16h; supports d, w, plus stdlib durations).
//	-project SUB   Only show sessions whose project path contains SUB.
//	-format FMT    Output format: text (default), json, jsonl.
//	-n N           Cap output to N sessions (default 50).
//	-v             Verbose: include the first user message.
//	-index         Read sessions-index.json instead of scanning every
//	               JSONL file. Faster but reports a smaller schema.
//
// # Examples
//
// List sessions from the last day:
//
//	ccsessions -since 24h
//
// Filter by project substring and emit JSON:
//
//	ccsessions -project myrepo -format json
//
// Use the cached index for a fast listing:
//
//	ccsessions -index -n 200
package main
