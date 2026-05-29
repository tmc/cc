package collector

import (
	"encoding/json"
	"strings"

	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccagentdef"
)

// ScanAgentDefs walks ~/.claude/agents (and .disabled/) and returns store
// records ready for upsert. Pass an empty root to use the default.
func ScanAgentDefs(root string) ([]store.AgentDef, error) {
	defs, err := listAgentDefs(root)
	if err != nil {
		return nil, err
	}
	out := make([]store.AgentDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, toStoreAgentDef(d))
	}
	return out, nil
}

func listAgentDefs(root string) ([]*ccagentdef.AgentDef, error) {
	if root == "" {
		return ccagentdef.ListAgentDefs()
	}
	return ccagentdef.ListAgentDefsIn(root)
}

func toStoreAgentDef(d *ccagentdef.AgentDef) store.AgentDef {
	if d == nil {
		return store.AgentDef{}
	}
	keywords, _ := json.Marshal(d.Triggers.Keywords)
	patterns, _ := json.Marshal(d.Triggers.Patterns)
	tools, _ := json.Marshal(d.Tools)
	caps, _ := json.Marshal(d.Capabilities)
	return store.AgentDef{
		Name:             d.Name,
		Description:      d.Description,
		Version:          d.Version,
		Command:          d.Command,
		Disabled:         d.Disabled,
		KeywordsJSON:     string(keywords),
		PatternsJSON:     string(patterns),
		ToolsJSON:        string(tools),
		CapabilitiesJSON: string(caps),
		SourcePath:       d.SourcePath,
		Searchable:       buildAgentSearchable(d),
	}
}

func buildAgentSearchable(d *ccagentdef.AgentDef) string {
	var parts []string
	parts = append(parts, d.Name, d.Description, d.Command)
	parts = append(parts, d.Triggers.Keywords...)
	parts = append(parts, d.Triggers.Patterns...)
	parts = append(parts, d.Capabilities...)
	parts = append(parts, d.Tools...)
	parts = append(parts, d.ExampleUsage...)
	parts = append(parts, d.Workflow...)
	return strings.Join(parts, "\n")
}
