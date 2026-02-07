// Command ccteam manages Claude Code agent teams.
//
// Ccteam creates, lists, inspects, and manages teams and their members.
// Teams are stored at ~/.claude/teams/{name}/config.json.
//
// # Usage
//
//	ccteam                                     # list all teams
//	ccteam -create NAME                        # create team
//	ccteam -show NAME                          # show team config
//	ccteam -delete NAME                        # delete team + data
//	ccteam -add NAME -agent AGENT [-model M]   # add member
//	ccteam -remove NAME -agent AGENT           # remove member
//	ccteam -format json                        # JSON output
//
// # Examples
//
// Create a team and add members:
//
//	ccteam -create review
//	ccteam -add review -agent reviewer -model claude-sonnet-4-5-20250929
//	ccteam -show review
//
// List all teams as JSON:
//
//	ccteam -format json
//
// Clean up:
//
//	ccteam -delete review
package main
