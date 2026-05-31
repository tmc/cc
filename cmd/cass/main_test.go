package main

import (
	"testing"

	"github.com/tmc/cc/cass"
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
