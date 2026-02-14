// Package collector provides session log collectors for various AI coding agents.
//
// Each collector implements [cass.Collector] and handles discovering and
// parsing session logs from a specific agent.
//
// Currently supported agents:
//   - Claude Code: ~/.claude/projects/ JSONL session files
package collector
