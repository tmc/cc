// Package collector provides session log collectors for various AI coding agents.
//
// Each collector implements [cass.Collector] and handles discovering and
// parsing session logs from a specific agent.
//
// Currently supported agents:
//   - Claude Code: ~/.claude/projects/ JSONL session files
//   - Codex: ~/.codex/sessions/ JSONL session files
//   - Gemini CLI: ~/.gemini/tmp/<project>/chats/ session JSON files
//   - Cursor: Cursor IDE workspace storage
//   - OpenClaw: ~/.openclaw/agents/<agent>/sessions/ JSONL files
//   - Antigravity: ~/.gemini/antigravity/ session data
package collector
