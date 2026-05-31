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
