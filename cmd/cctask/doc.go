// Command cctask manages persistent tasks for an agent team.
//
// cctask provides CRUD over task records stored under
// ~/.claude/tasks/<team>/. Each task has an ID, status, optional
// owner, and a `blocks` list naming task IDs that depend on it.
//
// # Usage
//
//	cctask -team TEAM -list
//	cctask -team TEAM -create -subject "Do X" [-desc "..."] [-assign AGENT]
//	cctask -team TEAM -show ID
//	cctask -team TEAM -update ID [flags...]
//	cctask -team TEAM -delete ID
//	cctask -team TEAM -wait ID [-timeout DUR]
//
// With no action flag, cctask prints the same listing as -list.
//
// # Flags
//
//	-team NAME       Team name (required).
//	-list            List every task.
//	-create          Create a new task; -subject is required.
//	-show ID         Show one task.
//	-update ID       Update one task; combine with the field flags below.
//	-delete ID       Delete a task.
//	-wait ID         Block until the task reaches status "completed".
//	-timeout DUR     Timeout for -wait (0 means wait forever).
//	-subject TEXT    Task subject (for -create / -update).
//	-desc TEXT       Task description (for -create / -update).
//	-status STATUS   pending, in_progress, or completed (for -update).
//	-assign AGENT    Owner agent name (for -create / -update).
//	-blocks IDS      Comma-separated task IDs this task blocks (for -update).
//	-format FMT      Output format: text (default), json.
//
// # Examples
//
// Create and assign a task:
//
//	cctask -team review -create -subject "Review PR #123" -assign reviewer
//
// Mark a task complete:
//
//	cctask -team review -update 1 -status completed
//
// Wait up to ten minutes for a task to finish:
//
//	cctask -team review -wait 1 -timeout 10m
package main
