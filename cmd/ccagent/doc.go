// Command ccagent inspects agent status and coordinates agents.
//
// Ccagent provides tools for monitoring agent processes, detecting idle agents,
// and streaming agent output. It uses PID files and inbox messages to determine
// agent state.
//
// # Usage
//
//	ccagent -team TEAM -list                   # list agents with status
//	ccagent -team TEAM -status AGENT           # show agent status
//	ccagent -team TEAM -idle                   # list idle agents
//	ccagent -team TEAM -wait-idle AGENT        # block until idle
//
// # Status Detection
//
// Agent status is determined by:
//
//   - PID file liveness (running vs dead)
//   - Inbox idle_notification messages
//   - Team config membership
//
// # Examples
//
// List all agents with their status:
//
//	ccagent -team review -list
//
// Wait for an agent to become idle:
//
//	ccagent -team review -wait-idle reviewer -timeout 5m
package main
