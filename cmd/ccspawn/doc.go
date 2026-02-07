// Command ccspawn spawns Claude Code agent processes in teammate mode.
//
// Ccspawn launches Claude Code CLI processes configured for multi-agent
// collaboration via the teammate protocol. Agents are spawned using a PTY
// wrapper to satisfy Claude Code's terminal requirements.
//
// # Usage
//
//	ccspawn -team TEAM -agent NAME [-model M] [-cwd DIR]
//	ccspawn -team TEAM -agent NAME -kill
//	ccspawn -team TEAM -list
//
// # Spawning
//
// When spawning an agent, ccspawn:
//
//  1. Registers the agent in the team config
//  2. Launches claude with --teammate-mode and agent flags
//  3. Records the PID at ~/.claude/teams/{team}/pids/{agent}.pid
//  4. Forwards stdout/stderr to the terminal
//
// The CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1 environment variable is set
// automatically.
//
// # Process Management
//
// Use -kill to send SIGTERM to an agent (SIGKILL after 5s).
// Use -list to show all agent processes and their liveness status.
//
// # Examples
//
// Spawn a reviewer agent:
//
//	ccspawn -team review -agent reviewer -model claude-sonnet-4-5-20250929
//
// Spawn in background:
//
//	ccspawn -team review -agent worker &
//
// Kill an agent:
//
//	ccspawn -team review -agent worker -kill
//
// List all agents:
//
//	ccspawn -team review -list
package main
