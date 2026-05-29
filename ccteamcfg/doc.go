// Package ccteamcfg reads and writes team configuration stored under
// ~/.claude/teams/<name>/config.json.
//
// A [TeamConfig] records a team's lead agent and its [TeamMember] roster.
// [ReadTeamConfig] and [WriteTeamConfig] load and persist it; [AddTeamMember]
// and [RemoveTeamMember] edit the roster in place. The CC_TEAMS_DIR environment
// variable overrides the base directory.
//
//	cfg, err := ccteamcfg.ReadTeamConfig("review")
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(cfg.LeadAgentID, len(cfg.Members))
package ccteamcfg
