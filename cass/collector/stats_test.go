package collector

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func toolUseEntry(name string, input map[string]any, ts time.Time) cc.Entry {
	inputJSON, _ := json.Marshal(input)
	blocks := []cc.ContentBlock{
		{Type: "tool_use", Name: name, Input: inputJSON},
	}
	content, _ := json.Marshal(blocks)
	return cc.Entry{
		Timestamp: ts,
		Type:      "assistant",
		Message: &cc.Message{
			Role:    "assistant",
			Content: content,
		},
	}
}

func userEntry(text string, ts time.Time) cc.Entry {
	content, _ := json.Marshal(text)
	return cc.Entry{
		Timestamp: ts,
		Type:      "user",
		Message: &cc.Message{
			Role:    "user",
			Content: content,
		},
	}
}

func teamEntry(e cc.Entry, teamName, agentName string) cc.Entry {
	e.TeamName = teamName
	e.AgentName = agentName
	return e
}

func TestClassifyTeamRole(t *testing.T) {
	ts := time.Date(2026, 2, 14, 21, 39, 0, 0, time.UTC)

	tests := []struct {
		name      string
		entries   []cc.Entry
		wantTeam  string
		wantAgent string
		wantLead  bool
	}{
		{
			name:      "no team session",
			entries:   []cc.Entry{userEntry("hello", ts)},
			wantTeam:  "",
			wantAgent: "",
			wantLead:  false,
		},
		{
			name: "team lead with TeamCreate",
			entries: []cc.Entry{
				teamEntry(toolUseEntry("TeamCreate", map[string]any{
					"team_name":   "work-team",
					"description": "test team",
				}, ts), "work-team", ""),
			},
			wantTeam:  "work-team",
			wantAgent: "",
			wantLead:  true,
		},
		{
			name: "team lead without TeamCreate but no agentName",
			entries: []cc.Entry{
				teamEntry(userEntry("start team", ts), "work-team", ""),
			},
			wantTeam:  "work-team",
			wantAgent: "",
			wantLead:  true,
		},
		{
			name: "team member with agentName",
			entries: []cc.Entry{
				teamEntry(userEntry("<teammate-message teammate_id=\"team-lead\">do work</teammate-message>", ts), "work-team", "researcher"),
			},
			wantTeam:  "work-team",
			wantAgent: "researcher",
			wantLead:  false,
		},
		{
			name: "contradictory entries: early teamName without agentName then agentName appears",
			entries: []cc.Entry{
				// First entry has teamName but no agentName (looks like lead).
				teamEntry(userEntry("starting up", ts), "work-team", ""),
				// Later entry adds agentName (actually a member).
				teamEntry(userEntry("doing work", ts.Add(time.Second)), "work-team", "researcher"),
			},
			wantTeam:  "work-team",
			wantAgent: "researcher",
			wantLead:  false,
		},
		{
			name: "contradictory entries: lead with TeamCreate overrides agentName",
			entries: []cc.Entry{
				// Has agentName set (unusual for lead, but TeamCreate is definitive).
				teamEntry(toolUseEntry("TeamCreate", map[string]any{
					"team_name": "work-team",
				}, ts), "work-team", "lead-agent"),
			},
			wantTeam:  "work-team",
			wantAgent: "lead-agent",
			wantLead:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			team, agent, lead := ClassifyTeamRole(tt.entries)
			if team != tt.wantTeam {
				t.Errorf("teamName = %q, want %q", team, tt.wantTeam)
			}
			if agent != tt.wantAgent {
				t.Errorf("agentName = %q, want %q", agent, tt.wantAgent)
			}
			if lead != tt.wantLead {
				t.Errorf("isLead = %v, want %v", lead, tt.wantLead)
			}
		})
	}
}

func TestExtractStats_TeamMessages(t *testing.T) {
	ts := time.Date(2026, 2, 14, 21, 39, 0, 0, time.UTC)

	entries := []cc.Entry{
		// Incoming teammate messages (2 in one user entry).
		teamEntry(userEntry(
			`<teammate-message teammate_id="researcher" color="blue" summary="ready">`+
				"I'm ready"+
				`</teammate-message>`+
				"\n\n"+
				`<teammate-message teammate_id="builder" color="green" summary="ready too">`+
				"Me too"+
				`</teammate-message>`,
			ts,
		), "work-team", ""),
		// SendMessage tool use.
		teamEntry(toolUseEntry("SendMessage", map[string]any{
			"type":      "message",
			"recipient": "researcher",
			"content":   "go investigate",
			"summary":   "task assignment",
		}, ts.Add(time.Second)), "work-team", ""),
		// TeamCreate tool use.
		teamEntry(toolUseEntry("TeamCreate", map[string]any{
			"team_name": "work-team",
		}, ts.Add(2*time.Second)), "work-team", ""),
		// Task tool use spawning a team member.
		teamEntry(toolUseEntry("Task", map[string]any{
			"name":          "reviewer",
			"team_name":     "work-team",
			"subagent_type": "general-purpose",
			"prompt":        "you are reviewer",
		}, ts.Add(3*time.Second)), "work-team", ""),
		// Regular Task (no team_name).
		toolUseEntry("Task", map[string]any{
			"prompt":        "research something",
			"subagent_type": "general-purpose",
		}, ts.Add(4*time.Second)),
	}

	stats := ExtractStats(entries)

	if stats.TeamMessagesRecvd != 2 {
		t.Errorf("TeamMessagesRecvd = %d, want 2", stats.TeamMessagesRecvd)
	}
	if stats.TeamInboxSends != 1 {
		t.Errorf("TeamInboxSends = %d, want 1", stats.TeamInboxSends)
	}
	if stats.TeamSpawns != 1 {
		t.Errorf("TeamSpawns = %d, want 1", stats.TeamSpawns)
	}
	if stats.TeamMembersSpawned != 1 {
		t.Errorf("TeamMembersSpawned = %d, want 1", stats.TeamMembersSpawned)
	}
	if stats.SubagentSpawns != 2 {
		t.Errorf("SubagentSpawns = %d, want 2 (both Task calls)", stats.SubagentSpawns)
	}
}

func TestExtractStats_AgentProgressMirrors(t *testing.T) {
	ts := time.Date(2026, 2, 14, 21, 39, 0, 0, time.UTC)
	mirroredData, _ := json.Marshal(map[string]any{
		"type":    "agent_progress",
		"agentId": "agent-a",
		"message": map[string]any{
			"uuid": "sub-1",
			"type": "assistant",
		},
	})
	orphanData, _ := json.Marshal(map[string]any{
		"type":    "agent_progress",
		"agentId": "agent-a",
		"message": map[string]any{
			"uuid": "missing",
			"type": "assistant",
		},
	})
	hookData, _ := json.Marshal(map[string]any{
		"type":      "hook_progress",
		"hookEvent": "PostToolUse",
	})

	entries := []cc.Entry{
		{
			Type:        "assistant",
			UUID:        "sub-1",
			IsSidechain: true,
			AgentID:     "agent-a",
			Timestamp:   ts,
			Message: &cc.Message{
				Role:    "assistant",
				Content: json.RawMessage(`[{"type":"text","text":"ok"}]`),
			},
		},
		{
			Type:        "user",
			UUID:        "sub-2",
			IsSidechain: true,
			AgentID:     "agent-a",
			Timestamp:   ts.Add(time.Second),
			Message: &cc.Message{
				Role:    "user",
				Content: json.RawMessage(`"next"`),
			},
		},
		{Type: "progress", UUID: "p1", Timestamp: ts.Add(2 * time.Second), Data: mirroredData},
		{Type: "progress", UUID: "p2", Timestamp: ts.Add(3 * time.Second), Data: orphanData},
		{Type: "progress", UUID: "p3", Timestamp: ts.Add(4 * time.Second), Data: hookData},
	}

	stats := ExtractStats(entries)
	if stats.SubagentEntries != 2 {
		t.Errorf("SubagentEntries = %d, want 2", stats.SubagentEntries)
	}
	if stats.SubagentMirroredEntries != 1 {
		t.Errorf("SubagentMirroredEntries = %d, want 1", stats.SubagentMirroredEntries)
	}
	if stats.AgentProgressEvents != 2 {
		t.Errorf("AgentProgressEvents = %d, want 2", stats.AgentProgressEvents)
	}
	if stats.AgentProgressMirrors != 1 {
		t.Errorf("AgentProgressMirrors = %d, want 1", stats.AgentProgressMirrors)
	}
	if stats.AgentProgressUnmatched != 1 {
		t.Errorf("AgentProgressUnmatched = %d, want 1", stats.AgentProgressUnmatched)
	}
}

func TestExtractTeamLinks(t *testing.T) {
	ts := time.Date(2026, 2, 14, 21, 39, 0, 0, time.UTC)

	t.Run("lead session links", func(t *testing.T) {
		entries := []cc.Entry{
			// TeamCreate (marks as lead).
			teamEntry(toolUseEntry("TeamCreate", map[string]any{
				"team_name": "work-team",
			}, ts), "work-team", ""),
			// Spawn researcher.
			teamEntry(toolUseEntry("Task", map[string]any{
				"name":      "researcher",
				"team_name": "work-team",
			}, ts.Add(time.Second)), "work-team", ""),
			// Spawn builder.
			teamEntry(toolUseEntry("Task", map[string]any{
				"name":      "builder",
				"team_name": "work-team",
			}, ts.Add(2*time.Second)), "work-team", ""),
			// Send message to researcher.
			teamEntry(toolUseEntry("SendMessage", map[string]any{
				"recipient": "researcher",
				"content":   "investigate X",
				"summary":   "investigate task",
			}, ts.Add(3*time.Second)), "work-team", ""),
			// Receive teammate message from researcher.
			teamEntry(userEntry(
				`<teammate-message teammate_id="researcher" color="blue" summary="done">`+
					"Found it"+
					`</teammate-message>`,
				ts.Add(4*time.Second),
			), "work-team", ""),
		}

		links := ExtractTeamLinks(entries)

		if len(links) != 4 {
			t.Fatalf("got %d links, want 4", len(links))
		}

		// Check spawn links.
		spawn1 := links[0]
		if spawn1.Action != "team-spawn" || spawn1.TargetSession != "researcher" {
			t.Errorf("link[0] = %s->%s (%s), want team-spawn->researcher", spawn1.SourceSession, spawn1.TargetSession, spawn1.Action)
		}
		if spawn1.Kind != "team" || spawn1.TeamName != "work-team" {
			t.Errorf("link[0] kind=%q team=%q, want team/work-team", spawn1.Kind, spawn1.TeamName)
		}
		spawn2 := links[1]
		if spawn2.Action != "team-spawn" || spawn2.TargetSession != "builder" {
			t.Errorf("link[1] = %s->%s (%s), want team-spawn->builder", spawn2.SourceSession, spawn2.TargetSession, spawn2.Action)
		}

		// Check message link.
		msg := links[2]
		if msg.Action != "team-message" || msg.SourceSession != "team-lead" || msg.TargetSession != "researcher" {
			t.Errorf("link[2] = %s->%s (%s), want team-lead->researcher (team-message)", msg.SourceSession, msg.TargetSession, msg.Action)
		}
		if msg.Text != "investigate task" {
			t.Errorf("link[2] text = %q, want %q", msg.Text, "investigate task")
		}

		// Check incoming message link.
		recv := links[3]
		if recv.Action != "team-message" || recv.SourceSession != "researcher" || recv.TargetSession != "team-lead" {
			t.Errorf("link[3] = %s->%s (%s), want researcher->team-lead (team-message)", recv.SourceSession, recv.TargetSession, recv.Action)
		}
	})

	t.Run("member session links", func(t *testing.T) {
		entries := []cc.Entry{
			// Initial prompt from team lead.
			teamEntry(userEntry(
				`<teammate-message teammate_id="team-lead">you are researcher</teammate-message>`,
				ts,
			), "work-team", "researcher"),
			// Member sends message to lead.
			teamEntry(toolUseEntry("SendMessage", map[string]any{
				"recipient": "team-lead",
				"content":   "ready for work",
				"summary":   "researcher ready",
			}, ts.Add(time.Second)), "work-team", "researcher"),
		}

		links := ExtractTeamLinks(entries)

		if len(links) != 2 {
			t.Fatalf("got %d links, want 2", len(links))
		}

		// Incoming from team-lead.
		if links[0].SourceSession != "team-lead" || links[0].TargetSession != "researcher" {
			t.Errorf("link[0] = %s->%s, want team-lead->researcher", links[0].SourceSession, links[0].TargetSession)
		}

		// Outgoing to team-lead.
		if links[1].SourceSession != "researcher" || links[1].TargetSession != "team-lead" {
			t.Errorf("link[1] = %s->%s, want researcher->team-lead", links[1].SourceSession, links[1].TargetSession)
		}
	})

	t.Run("non-team session returns nil", func(t *testing.T) {
		entries := []cc.Entry{
			userEntry("hello", ts),
		}
		links := ExtractTeamLinks(entries)
		if links != nil {
			t.Errorf("got %d links, want nil for non-team session", len(links))
		}
	})
}

func TestExtractStats_IT2AdHocTeamSignals(t *testing.T) {
	ts := time.Date(2026, 2, 14, 10, 0, 0, 0, time.UTC)

	entries := []cc.Entry{
		// Split creates a new pane (strong team signal).
		bashToolUseEntry("assistant", `it2 session split -q --vertical`, ts),
		// Send text to the new pane.
		bashToolUseEntry("assistant", `it2 session send-text "D9DD130C" "do the work"`, ts.Add(time.Second)),
		// Check on it.
		bashToolUseEntry("assistant", `it2 session get-screen "D9DD130C"`, ts.Add(2*time.Second)),
	}

	stats := ExtractStats(entries)

	if stats.IT2Splits != 1 {
		t.Errorf("IT2Splits = %d, want 1", stats.IT2Splits)
	}
	if stats.IT2Sends != 1 {
		t.Errorf("IT2Sends = %d, want 1", stats.IT2Sends)
	}
	if stats.IT2Screens != 1 {
		t.Errorf("IT2Screens = %d, want 1", stats.IT2Screens)
	}
}
