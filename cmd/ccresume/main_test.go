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

func TestRenderResumeCommandQuotesSpaces(t *testing.T) {
	got := renderResumeCommand("/Volumes/My Disk/proj", "claude", []string{"-r", "sid 1"})
	want := "cd '/Volumes/My Disk/proj'; claude -r 'sid 1'"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestRenderResumeCommandQuotesShellMetachars(t *testing.T) {
	got := renderResumeCommand("/work/proj's; rm -rf /", "claude", nil)
	want := `cd '/work/proj'\''s; rm -rf /'; claude`
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

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: stale})
	if r.target != live {
		t.Fatalf("target = %q, want %q", r.target, live)
	}
}

func TestResolveMatchPrefersExistingProjectPathOverLaterCWD(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	nested := filepath.Join(project, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": project},
		{"sessionId": "s1", "cwd": nested},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: project})
	if r.target != project {
		t.Fatalf("target = %q, want %q", r.target, project)
	}
}

func TestResolveMatchIgnoresCodexSessionStorageProjectPath(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	storage := filepath.Join(dir, ".codex", "sessions", "2026", "05", "23")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(storage, "rollout-2026-05-23T00-00-00-s1.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": project},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: storage})
	if r.target != project {
		t.Fatalf("target = %q, want %q", r.target, project)
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

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: dir})
	if r.target != dir {
		t.Fatalf("target = %q, want %q", r.target, dir)
	}
}

func TestPreferredProjectPathUsesHomeGoSrcSymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	target := filepath.Join(root, "vol", "go", "src")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "github.com", "tmc", "appledocs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "go", "src")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got := preferredProjectPath(filepath.Join(target, "github.com", "tmc", "appledocs"))
	want := filepath.Join(home, "go", "src", "github.com", "tmc", "appledocs")
	if got != want {
		t.Fatalf("preferredProjectPath = %q, want %q", got, want)
	}
}

func TestCountOccurrences(t *testing.T) {
	tests := []struct {
		name  string
		query string
		text  string
		want  int
	}{
		{
			name:  "case insensitive",
			query: "rdma",
			text:  "RDMA setup mentioned rdma twice",
			want:  2,
		},
		{
			name:  "non overlapping",
			query: "aa",
			text:  "aaaa",
			want:  2,
		},
		{
			name:  "empty query",
			query: "",
			text:  "rdma",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countOccurrences(tt.query, tt.text); got != tt.want {
				t.Fatalf("countOccurrences(%q, %q) = %d, want %d", tt.query, tt.text, got, tt.want)
			}
		})
	}
}

func TestFileHasToolMatchCommandAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := []byte(
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"it2 session current"}}]}}` + "\n" +
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"052AB527-F732-4D5A-A35D-D72E2EF98137"}]}}` + "\n")
	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatal(err)
	}
	ok, err := fileHasToolMatch(path, "it2 session current", "052AB527")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("tool command/result match not found")
	}
	ok, err = fileHasToolMatch(path, "it2 session current", "missing-session")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unexpected tool command/result match")
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
