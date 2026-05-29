package collector

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// subagentNotificationRE captures the JSON object inside a codex
// <subagent_notification> block emitted into a user message.
var subagentNotificationRE = regexp.MustCompile(`(?s)<subagent_notification>\s*(\{.*?\})\s*</subagent_notification>`)

// extractCodexSubagentRuns builds SubagentRun records for the agents a codex
// session spawned. Codex spawns via the spawn_agent tool, whose output returns
// the assigned agent_id, and reports completion through <subagent_notification>
// user messages whose agent_path is that same id — the spawned agent's own
// session id (a peer rollout file). Runs are joined by that id, not by spawn
// order: spawns and completions interleave, and a rejected spawn returns an
// error instead of an id.
//
// Unlike Claude Code, the spawned transcript is a peer session indexed on its
// own, so SourcePath is left empty here and the AgentID links the two sessions.
func extractCodexSubagentRuns(entries []cc.Entry, parent cass.Session) []cass.SubagentRun {
	runs := map[string]*cass.SubagentRun{}
	get := func(id string) *cass.SubagentRun {
		if r := runs[id]; r != nil {
			return r
		}
		r := &cass.SubagentRun{
			AgentID:         id,
			ParentSessionID: parent.ID,
			Workspace:       parent.Workspace,
			Status:          "unknown",
		}
		runs[id] = r
		return r
	}

	// First pass: collect spawn_agent agent types keyed by call id. The output
	// (which carries the assigned agent id) arrives as a separate tool-result
	// entry, so it is handled in the second pass.
	spawnType := map[string]string{}
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "assistant" {
			continue
		}
		for _, b := range e.Message.ToolUses() {
			if b.Name == "spawn_agent" {
				spawnType[b.ID] = codexSpawnAgentType(b.Input)
			}
		}
	}

	// Second pass: join spawn outputs (call id -> agent id) and notifications
	// (agent id -> status) onto runs keyed by the agent id.
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		for _, tr := range e.Message.ToolResults() {
			if _, ok := spawnType[tr.ToolUseID]; !ok {
				continue
			}
			id, nickname := codexSpawnAgentResult(tr.Content)
			if id == "" {
				continue // rejected spawn (error string, no id).
			}
			r := get(id)
			r.AgentType = spawnType[tr.ToolUseID]
			r.Description = nickname
			if r.StartedAt.IsZero() {
				r.StartedAt = e.Timestamp
			}
		}
		if e.Message.Role != "user" {
			continue
		}
		for _, m := range subagentNotificationRE.FindAllStringSubmatch(e.Message.TextContent(), -1) {
			id, status := codexSubagentNotification(m[1])
			if id == "" {
				continue
			}
			r := get(id)
			if status != "" {
				r.Status = status
			}
			if !e.Timestamp.IsZero() {
				if r.StartedAt.IsZero() {
					r.StartedAt = e.Timestamp
				}
				r.EndedAt = e.Timestamp
			}
		}
	}

	if len(runs) == 0 {
		return nil
	}
	out := make([]cass.SubagentRun, 0, len(runs))
	for _, r := range runs {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.Before(out[j].StartedAt)
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}

func codexSpawnAgentType(raw json.RawMessage) string {
	var in struct {
		AgentType string `json:"agent_type"`
	}
	_ = json.Unmarshal(raw, &in)
	return strings.TrimSpace(in.AgentType)
}

// codexSpawnAgentResult parses a spawn_agent tool output. A successful spawn
// returns {"agent_id":"...","nickname":"..."}; a rejected one returns a plain
// error string, yielding an empty id.
func codexSpawnAgentResult(content string) (agentID, nickname string) {
	var out struct {
		AgentID  string `json:"agent_id"`
		Nickname string `json:"nickname"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &out) != nil {
		return "", ""
	}
	return out.AgentID, out.Nickname
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
