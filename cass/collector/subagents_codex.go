package collector

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// subagentNotificationRE captures the JSON object inside a codex
// <subagent_notification> block emitted into a user message.
var subagentNotificationRE = regexp.MustCompile(`(?s)<subagent_notification>\s*(\{.*?\})\s*</subagent_notification>`)

// extractCodexSubagentRuns builds SubagentRun records for the agents a codex
// session spawned. Codex spawns via the spawn_agent tool and reports completion
// through <subagent_notification> user messages whose agent_path is the spawned
// agent's own session id (a peer rollout file). The notification is the reliable
// record; spawn_agent calls supply the agent type in spawn order.
//
// Unlike Claude Code, the spawned transcript is a peer session indexed on its
// own, so SourcePath is left empty here and the AgentID links the two sessions.
func extractCodexSubagentRuns(entries []cc.Entry, parent cass.Session) []cass.SubagentRun {
	var types []string
	runs := map[string]*cass.SubagentRun{}
	var order []string

	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "assistant":
			for _, b := range e.Message.ToolUses() {
				if b.Name == "spawn_agent" {
					types = append(types, codexSpawnAgentType(b.Input))
				}
			}
		case "user":
			for _, m := range subagentNotificationRE.FindAllStringSubmatch(e.Message.TextContent(), -1) {
				id, status := codexSubagentNotification(m[1])
				if id == "" {
					continue
				}
				run := runs[id]
				if run == nil {
					run = &cass.SubagentRun{
						AgentID:         id,
						ParentSessionID: parent.ID,
						Workspace:       parent.Workspace,
						Status:          "unknown",
					}
					runs[id] = run
					order = append(order, id)
				}
				if status != "" {
					run.Status = status
				}
				if !e.Timestamp.IsZero() {
					if run.StartedAt.IsZero() {
						run.StartedAt = e.Timestamp
					}
					run.EndedAt = e.Timestamp
				}
			}
		}
	}

	if len(runs) == 0 {
		return nil
	}

	// Assign spawn-order agent types to runs in notification order; a missing
	// type simply leaves AgentType empty.
	out := make([]cass.SubagentRun, 0, len(order))
	for i, id := range order {
		run := runs[id]
		if i < len(types) {
			run.AgentType = types[i]
		}
		out = append(out, *run)
	}
	return out
}

func codexSpawnAgentType(raw json.RawMessage) string {
	var in struct {
		AgentType string `json:"agent_type"`
	}
	_ = json.Unmarshal(raw, &in)
	return strings.TrimSpace(in.AgentType)
}

// codexSubagentNotification extracts the spawned agent's session id and a
// coarse status from a <subagent_notification> payload. status is a single-key
// object such as {"completed": "..."}; the key is the status.
func codexSubagentNotification(payload string) (agentID, status string) {
	var n struct {
		AgentPath string                     `json:"agent_path"`
		Status    map[string]json.RawMessage `json:"status"`
	}
	if json.Unmarshal([]byte(payload), &n) != nil {
		return "", ""
	}
	for k := range n.Status {
		status = k
		break
	}
	return n.AgentPath, status
}
