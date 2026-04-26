package main

import (
	"encoding/json"
	"os"
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

func TestResolveMatchPrefersLatestExistingCWD(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale-does-not-exist")
	live := filepath.Join(dir, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": stale},
		{"sessionId": "s1", "cwd": live},
	})

	r := resolveMatch(cc.IndexEntry{FullPath: session, ProjectPath: stale})
	if r.target != live {
		t.Fatalf("target = %q, want %q", r.target, live)
	}
}

func TestResolveMatchFallsBackToProjectPath(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone-1")
	gone2 := filepath.Join(dir, "gone-2")
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": gone},
		{"sessionId": "s1", "cwd": gone2},
	})

	r := resolveMatch(cc.IndexEntry{FullPath: session, ProjectPath: dir})
	if r.target != dir {
		t.Fatalf("target = %q, want %q", r.target, dir)
	}
}

func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
}
