// Package ccagentdef reads user-defined agent templates stored under
// ~/.claude/agents/.
//
// An [AgentDef] declares an agent's triggers, capabilities, and tool surface.
// [ListAgentDefs] reads from the default agents directory and [ListAgentDefsIn]
// from an explicit root; [ReadAgentDef] loads a single template.
//
//	defs, err := ccagentdef.ListAgentDefs()
//	if err != nil {
//		log.Fatal(err)
//	}
//	for _, d := range defs {
//		fmt.Println(d.Name, d.Description)
//	}
package ccagentdef
