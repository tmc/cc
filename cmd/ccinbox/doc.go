// Command ccinbox sends and reads agent inbox messages.
//
// ccinbox writes to and reads from JSON inboxes stored at
// ~/.claude/teams/<team>/inboxes/<agent>.json. The body of a sent
// message is read from stdin.
//
// # Usage
//
//	echo "hello" | ccinbox -team TEAM -to AGENT
//	ccinbox -team TEAM -from AGENT [-unread|-follow]
//	ccinbox -team TEAM -broadcast "text"
//	ccinbox -team TEAM -to AGENT -type task_assignment -task-id 3 -subject S
//
// # Flags
//
//	-team NAME       Team name (required).
//	-to AGENT        Send to AGENT; body is read from stdin.
//	-from AGENT      Read AGENT's inbox.
//	-unread          With -from, show only unread messages.
//	-follow          With -from, poll for and stream new messages.
//	-broadcast TEXT  Send TEXT to every member of the team.
//	-sender NAME     Sender name attached to outgoing messages
//	                 (default "controller").
//	-type TYPE       Wrap stdin as a structured message of TYPE; see below.
//	-task-id ID      taskId for -type task_assignment.
//	-subject TEXT    subject for -type task_assignment.
//	-tool-name NAME  toolName for -type permission_request.
//	-format FMT      Output format for reads: text (default), json.
//
// # Structured Message Types
//
// With -type, stdin becomes the body of a JSON message of one of:
//
//	plain_text             text only
//	task_assignment        requires -task-id and -subject
//	shutdown_request       body becomes the reason
//	idle_notification      body becomes the idle reason
//	plan_approval_request  body becomes planContent
//	permission_request     requires -tool-name
//
// # Examples
//
// Send a plain message:
//
//	echo "review PR #123" | ccinbox -team review -to reviewer
//
// Read unread messages from a controller inbox:
//
//	ccinbox -team review -from controller -unread
//
// Tail an inbox:
//
//	ccinbox -team review -from controller -follow
package main
