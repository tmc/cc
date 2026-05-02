// Command ccapprove handles plan and permission approval requests.
//
// ccapprove inspects a controller-style inbox for pending
// `plan_approval_request` and `permission_request` messages, then
// records `*_response` replies into both the controller inbox and
// the requesting agent's inbox.
//
// # Usage
//
//	ccapprove -team TEAM [flags]
//
// # Flags
//
//	-team NAME       Team name (required).
//	-list            List pending approval requests (default action).
//	-approve AGENT   Approve the pending request from AGENT.
//	-deny AGENT      Deny the pending request from AGENT.
//	-reason TEXT     Feedback string attached to a -deny response.
//	-auto            Daemon mode: auto-approve every request as it arrives.
//	-follow          Stream new pending requests as they appear.
//	-inbox NAME      Inbox to monitor (default "controller").
//	-format FMT      Output format: text (default), json.
//
// # Examples
//
// Show what is waiting on the controller:
//
//	ccapprove -team review -list
//
// Approve the reviewer's pending plan:
//
//	ccapprove -team review -approve reviewer
//
// Deny with feedback:
//
//	ccapprove -team review -deny reviewer -reason "needs tests"
//
// Run the auto-approve daemon (e.g. for unattended runs):
//
//	ccapprove -team review -auto
package main
