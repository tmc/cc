// Command ccinbox reads and writes agent inbox messages.
//
// Ccinbox sends messages to and reads messages from agent inboxes.
// Inboxes are stored at ~/.claude/teams/{teamName}/inboxes/{agentName}.json.
// Message body is read from stdin for send operations.
//
// # Usage
//
//	echo "do the thing" | ccinbox -team TEAM -to AGENT       # send
//	ccinbox -team TEAM -from AGENT                           # read all
//	ccinbox -team TEAM -from AGENT -unread                   # unread only
//	ccinbox -team TEAM -from AGENT -follow                   # tail -f style
//	ccinbox -team TEAM -broadcast "message"                  # to all agents
//	ccinbox -team TEAM -to AGENT -type task_assignment -task-id 3
//
// # Message Types
//
// The -type flag sends structured messages:
//
//   - plain_text: default, message body is the text
//   - task_assignment: requires -task-id, -subject
//   - shutdown_request: sends shutdown request
//   - idle_notification: sends idle notification
//   - plan_approval_request: sends plan approval request
//   - permission_request: requires -tool-name
//
// # Examples
//
// Send a plain message:
//
//	echo "review PR #123" | ccinbox -team review -to reviewer
//
// Read unread messages:
//
//	ccinbox -team review -from controller -unread
//
// Follow an inbox for new messages:
//
//	ccinbox -team review -from controller -follow
package main
