// Command cctask manages persistent tasks for agent teams.
//
// Cctask provides CRUD operations on task files stored at
// ~/.claude/tasks/{teamName}/{taskId}.json. Tasks support status tracking,
// ownership, and dependency management via blocks/blockedBy relationships.
//
// # Usage
//
//	cctask -team TEAM -list
//	cctask -team TEAM -create -subject "Do X" [-desc "..."]
//	cctask -team TEAM -show ID
//	cctask -team TEAM -update ID -status completed
//	cctask -team TEAM -update ID -assign AGENT
//	cctask -team TEAM -update ID -blocks 4,5
//	cctask -team TEAM -delete ID
//	cctask -team TEAM -wait ID [-timeout 5m]
//
// # Task Status
//
// Tasks have three possible statuses:
//
//   - pending: not yet started
//   - in_progress: being worked on
//   - completed: finished
//
// # Examples
//
// Create and assign a task:
//
//	cctask -team review -create -subject "Review PR #123"
//	cctask -team review -update 1 -assign reviewer -status in_progress
//
// Wait for task completion:
//
//	cctask -team review -wait 1 -timeout 10m
//
// List all tasks as JSON:
//
//	cctask -team review -list -format json
package main
