package main

import (
	"testing"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/store"
)

func TestResumeCommandQuotesWorkspace(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid-123",
		Agent:     "codex-cli",
		Workspace: "/Volumes/My Disk/proj",
	})
	want := "cd '/Volumes/My Disk/proj' && codex resume sid-123"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandQuotesShellMetachars(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid 123",
		Agent:     "codex-app",
		Workspace: "/work/proj's; rm -rf /",
	})
	want := "cd '/work/proj'\\''s; rm -rf /' && codex resume 'sid 123'"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandLeavesSafeWorkspaceBare(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid-123",
		Agent:     "codex-cli",
		Workspace: "/work/proj",
	})
	want := "cd /work/proj && codex resume sid-123"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandUsesClaudeSourcePathID(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID:  "cass-sha-id",
		Agent:      "claude-code",
		Workspace:  "/work/proj",
		SourcePath: "/Users/me/.claude/projects/-work-proj/11111111-2222-3333-4444-555555555555.jsonl",
	})
	want := "cd /work/proj && claude --resume 11111111-2222-3333-4444-555555555555"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandSkipsClaudeSubagentPath(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID:  "cass-sha-id",
		Agent:      "claude-code",
		Workspace:  "/work/proj",
		SourcePath: "/Users/me/.claude/projects/-work-proj/11111111/subagents/agent-worker.jsonl",
	})
	want := "cd /work/proj && claude --resume"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestWorkflowEntryFromRowDecodesPhasesAndAgents(t *testing.T) {
	got := workflowEntryFromRow(store.WorkflowRow{
		ParentSessionID: "parent-1",
		RunID:           "wf_1",
		Name:            "review workflow",
		Status:          "completed",
		AgentCount:      2,
		PhasesJSON:      `[{"title":"Review","detail":"lens pass"}]`,
		AgentsJSON:      `[{"id":"agent-a","label":"lens:api","phase":"Review","agent_type":"Explore"}]`,
	})
	if got.ParentSessionID != "parent-1" || got.RunID != "wf_1" || got.Name != "review workflow" {
		t.Fatalf("workflow entry identity = %+v", got)
	}
	if len(got.Phases) != 1 || got.Phases[0].Title != "Review" || got.Phases[0].Detail != "lens pass" {
		t.Fatalf("phases = %+v", got.Phases)
	}
	if len(got.Agents) != 1 || got.Agents[0].Label != "lens:api" ||
		got.Agents[0].Phase != "Review" || got.Agents[0].AgentType != "Explore" {
		t.Fatalf("agents = %+v", got.Agents)
	}
}

func TestWorkflowAgentLabelsSkipPromptTitles(t *testing.T) {
	got := workflowAgentLabels([]cass.WorkflowAgent{{
		ID:        "agent-a",
		Title:     "You are reviewing this workflow transcript. READ THESE FILES FIRST before responding.",
		AgentType: "Explore",
	}})
	if len(got) != 1 || got[0] != "Explore" {
		t.Fatalf("workflowAgentLabels = %v, want [Explore]", got)
	}
}
