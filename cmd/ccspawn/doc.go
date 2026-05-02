// Command ccspawn launches Claude Code agent processes in teammate mode.
//
// ccspawn registers an agent in the team config, execs the `claude`
// binary with the appropriate `--teammate-mode` and identity flags,
// records the agent PID under ~/.claude/teams/<team>/pids/, and
// forwards stdin/stdout/stderr until the agent exits.
// CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 is added to the agent's
// environment.
//
// # Usage
//
//	ccspawn -team TEAM -agent NAME [flags]
//	ccspawn -team TEAM -agent NAME -kill
//	ccspawn -team TEAM -list
//
// # Flags
//
//	-team NAME             Team name (required).
//	-agent NAME            Agent name (required for spawn / -kill).
//	-model M               Model to pass through as --model.
//	-cwd DIR               Working directory for the agent (default: cwd).
//	-type TYPE             Agent type (default "general-purpose").
//	-permission-mode MODE  Pass through as --permission-mode.
//	-allowed-tools LIST    Comma-separated --allowedTools entries.
//	-claude-binary NAME    Binary to exec (default "claude", looked up on PATH).
//	-kill                  Send SIGTERM to the agent (SIGKILL after 5s).
//	-list                  List agent PIDs and liveness for the team.
//
// # Examples
//
// Spawn a reviewer agent on a specific model:
//
//	ccspawn -team review -agent reviewer -model claude-sonnet-4-5-20250929
//
// Limit a worker to a tool subset:
//
//	ccspawn -team review -agent worker -allowed-tools Read,Grep,Bash
//
// Show liveness:
//
//	ccspawn -team review -list
//
// Kill a stuck agent:
//
//	ccspawn -team review -agent worker -kill
package main
