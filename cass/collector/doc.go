// Package collector provides session log collectors for various AI coding agents.
//
// Each collector implements [cass.Collector] and handles discovering and
// parsing session logs from a specific agent.
//
// # Collectors
//
//   - [ClaudeCode]: ~/.claude/projects/ JSONL session files
//   - [Codex]: ~/.codex/sessions/ JSONL session files
//   - [GeminiCLI]: ~/.gemini/tmp/<project>/chats/ session JSON files
//   - [Cursor]: Cursor IDE workspace storage
//   - [OpenClaw]: ~/.openclaw/agents/<agent>/sessions/ JSONL files
//   - [OpenCode]: ~/.local/share/opencode/storage session data
//   - [Antigravity]: ~/.gemini/antigravity/ session data
//   - [Pi]: ~/.pi/agent/sessions/ JSONL session files
//
// # Derived-data helpers
//
// Scanners walk on-disk state outside the per-session JSONL stream:
//
//   - [ScanAgentDefs]: ~/.claude/agents/**/*.md agent definitions
//   - [ScanJobs]: ~/.claude/jobs/<shortId>/state.json job records
//   - [ScanTeamConfigs]: ~/.claude/teams/<name>/config.json team rosters
//
// Extractors derive structured records from a session's parsed entries:
//
//   - [ExtractClaudeGoals]: goal-mode objectives
//   - [ExtractLinks]: inter-session message/observation links
//   - [ExtractTeamLinks]: team-membership links
//   - [ExtractSkills]: skill invocations
//   - [ExtractStats]: per-session aggregate statistics
package collector
