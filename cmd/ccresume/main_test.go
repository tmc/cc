package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tmc/cc"
)

func TestResumeInvocation(t *testing.T) {
	tests := []struct {
		name     string
		entry    cc.IndexEntry
		wantBin  string
		wantArgs []string
	}{
		{
			name: "codex",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".codex", "sessions", "2026", "02", "25", "rollout.jsonl"),
				SessionID: "codex-session-id",
			},
			wantBin:  "codex",
			wantArgs: []string{"resume", "codex-session-id"},
		},
		{
			name: "gemini",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".gemini", "projects", "p", "s.jsonl"),
				SessionID: "gemini-session-id",
			},
			wantBin:  "gemini",
			wantArgs: []string{"-r", "gemini-session-id"},
		},
		{
			name: "claude",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".claude", "projects", "p", "s.jsonl"),
				SessionID: "claude-session-id",
			},
			wantBin:  "claude",
			wantArgs: []string{"-r", "claude-session-id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBin, gotArgs := resumeInvocation(tt.entry)
			if gotBin != tt.wantBin {
				t.Fatalf("bin = %q, want %q", gotBin, tt.wantBin)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestRenderResumeCommand(t *testing.T) {
	got := renderResumeCommand("/work/proj", "codex", []string{"resume", "sid-123"})
	want := "cd /work/proj; codex resume sid-123"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
