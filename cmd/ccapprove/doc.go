// Command ccapprove handles plan and permission approval workflows.
//
// Ccapprove monitors agent inboxes for approval requests and sends approval
// or denial responses. It reads permission_request and plan_approval_request
// messages from the controller inbox and writes responses to agent inboxes.
//
// # Usage
//
//	ccapprove -team TEAM -list                 # pending approvals
//	ccapprove -team TEAM -approve AGENT        # approve pending request
//	ccapprove -team TEAM -deny AGENT           # deny with optional -reason
//	ccapprove -team TEAM -auto                 # auto-approve daemon
//	ccapprove -team TEAM -follow               # watch for requests
//
// # Auto-Approve Mode
//
// In auto-approve mode (-auto), ccapprove watches for incoming approval
// requests and automatically approves them. This is useful for unattended
// operation.
//
// # Examples
//
// List pending approvals:
//
//	ccapprove -team review -list
//
// Approve a specific agent's request:
//
//	ccapprove -team review -approve reviewer
//
// Deny with reason:
//
//	ccapprove -team review -deny reviewer -reason "needs tests"
//
// Run auto-approve daemon:
//
//	ccapprove -team review -auto &
package main
