// Command ccagent inspects agent processes and idleness for a team.
//
// ccagent reads the team config at ~/.claude/teams/<team>/config.json
// and inspects, for each member, the PID file under pids/ and the
// most recent structured message in the agent's inbox to determine
// whether the agent is running, dead, or idle.
//
// # Usage
//
//	ccagent -team TEAM [flags]
//
// With no action flag, ccagent prints the same listing as -list.
//
// # Flags
//
//	-team NAME            Team name (required).
//	-list                 List every agent with status.
//	-status NAME          Show detailed status for one agent.
//	-idle                 List only agents that are idle.
//	-wait-idle NAME       Block until NAME is idle (poll once per second).
//	-timeout DUR          Timeout for -wait-idle (0 means wait forever).
//	-format FMT           Output format: text (default), json.
//
// # Status Detection
//
// An agent is "running" if its pid file points to a live process,
// "dead" otherwise. It is "idle" if the most recent structured inbox
// message is an `idle_notification` not yet superseded by a
// `task_assignment` or `plain_text` message.
//
// # Examples
//
// Show every agent on a team:
//
//	ccagent -team review -list
//
// Wait up to five minutes for an agent to go idle:
//
//	ccagent -team review -wait-idle reviewer -timeout 5m
//
// Emit machine-readable status:
//
//	ccagent -team review -status reviewer -format json
package main
