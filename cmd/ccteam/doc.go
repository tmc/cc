// Command ccteam manages Claude Code agent teams.
//
// ccteam creates, lists, inspects, and modifies teams whose configs
// live at ~/.claude/teams/<name>/config.json. New members default to
// agent type "general-purpose" and the current working directory.
//
// # Usage
//
//	ccteam                                  List all teams (default).
//	ccteam -create NAME                     Create an empty team.
//	ccteam -show NAME                       Show one team's config.
//	ccteam -delete NAME                     Delete a team and its data.
//	ccteam -add NAME -agent A [-model M] [-cwd DIR]
//	                                        Add a member to NAME.
//	ccteam -remove NAME -agent A            Remove a member from NAME.
//
// # Flags
//
//	-create NAME    Create a team called NAME.
//	-show NAME      Print the team's config.
//	-delete NAME    Delete the team and its inbox/pid data.
//	-add NAME       Add a member to team NAME (requires -agent).
//	-remove NAME    Remove a member (requires -agent).
//	-agent NAME     Agent name for -add / -remove.
//	-model M        Model id passed to the agent at spawn time.
//	-cwd DIR        Working directory for the agent (default: cwd).
//	-format FMT     Output format: text (default), json.
//
// # Examples
//
// Create a team and add a reviewer:
//
//	ccteam -create review
//	ccteam -add review -agent reviewer -model claude-sonnet-4-5-20250929
//
// List teams as JSON:
//
//	ccteam -format json
//
// Tear down:
//
//	ccteam -delete review
package main
