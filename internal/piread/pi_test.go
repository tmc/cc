package piread

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsPiSessionPath(t *testing.T) {
	// Fast-path cases that do not require a header sniff (agent+sessions
	// adjacency), plus negatives. Sniff-only cases (sessions present but no
	// agent parent) are covered by TestIsPiSessionPathSniff with real files.
	tests := []struct {
		path string
		want bool
	}{
		{"/home/u/.pi/agent/sessions/--p--/ts_id.jsonl", true},
		{"/custom/agent/sessions/proj/x.jsonl", true},
		{"/a/agent/sessions/proj/x.jsonl", true},           // shallow agent/sessions/<proj>/<file> (off-by-one regression).
		{"/home/u/.claude/projects/-enc/abc.jsonl", false}, // claude jsonl, no agent/sessions.
		{"/home/u/.pi/agent/sessions/--p--/x.json", false}, // not jsonl.
		{"/agent/sessions/x.jsonl", false},                 // file directly under sessions, no project dir.
	}
	for _, tt := range tests {
		if got := IsSessionPath(tt.path); got != tt.want {
			t.Errorf("IsSessionPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsPiSessionPathSniff(t *testing.T) {
	// A PI_CODING_AGENT_DIR override whose dir is not named "agent": detection
	// must fall back to sniffing the session header, including for shallow
	// sessions/ paths that the loop bound previously skipped.
	root := t.TempDir()
	piFile := filepath.Join(root, "sessions", "proj", "x.jsonl")
	writeTestFile(t, piFile, `{"type":"session","id":"s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/w"}`+"\n")
	if !IsSessionPath(piFile) {
		t.Errorf("IsSessionPath(%q) = false, want true (sniff)", piFile)
	}

	// A codex-style file under sessions/ whose first line is session_meta, not
	// session, must not be mistaken for pi.
	codexFile := filepath.Join(root, "sessions", "2026", "rollout.jsonl")
	writeTestFile(t, codexFile, `{"type":"session_meta","payload":{}}`+"\n")
	if IsSessionPath(codexFile) {
		t.Errorf("IsSessionPath(%q) = true, want false (codex session_meta)", codexFile)
	}
}

func TestReadFilePiErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "pi session empty"},
		{"missing header", `{"type":"message","id":"a"}` + "\n", "pi session missing header"},
		{"not session type", `{"type":"model_change"}` + "\n", "pi session missing header"},
		{"missing id", `{"type":"session","timestamp":"2026-04-19T00:00:00.000Z"}` + "\n", "pi session missing id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTestFile(t, path, tt.content)
			_, err := ReadFile(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReadFile err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPiToolName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"bash", "Bash"},
		{"edit", "Edit"},
		{"write", "Write"},
		{"read", "Read"},
		{"grep", "Grep"},
		{"glob", "Glob"},
		{"find", "Glob"},
		{"ls", "LS"},
		{"GREP", "Grep"},     // case-insensitive.
		{"mytool", "mytool"}, // custom names pass through unchanged.
	}
	for _, tt := range tests {
		if got := piToolName(tt.in); got != tt.want {
			t.Errorf("piToolName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
