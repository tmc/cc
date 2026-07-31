package ccpaths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPiEncodePath(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/private/tmp", "--private-tmp--"},
		{"/Users/example/go/src/github.com/tmc/go-pi", "--Users-example-go-src-github.com-tmc-go-pi--"},
	}
	for _, tt := range tests {
		if got := PiEncodePath(tt.path); got != tt.want {
			t.Errorf("PiEncodePath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestProjectDir(t *testing.T) {
	t.Setenv("CLAUDE_HOME", "/home/u/.claude")
	t.Setenv("GEMINI_HOME", "/home/u/.gemini")
	t.Setenv("PI_CODING_AGENT_DIR", "/home/u/.pi/agent")

	tests := []struct {
		harness, want string
	}{
		{"claude", filepath.Join("/home/u/.claude", "projects", "-work-proj")},
		{"gemini", filepath.Join("/home/u/.gemini", "projects", "-work-proj")},
		{"pi", filepath.Join("/home/u/.pi/agent", "sessions", "--work-proj--")},
	}
	for _, tt := range tests {
		got, err := ProjectDir(tt.harness, "/work/proj")
		if err != nil {
			t.Errorf("ProjectDir(%q): %v", tt.harness, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ProjectDir(%q) = %q, want %q", tt.harness, got, tt.want)
		}
	}

	for _, harness := range []string{"codex", "opencode", "antigravity"} {
		if _, err := ProjectDir(harness, "/work/proj"); err == nil {
			t.Errorf("ProjectDir(%q) = nil error, want no-per-project-directory error", harness)
		}
	}
	if _, err := ProjectDir("bogus", "/work/proj"); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("ProjectDir(bogus) err = %v, want unknown harness", err)
	}
}

func TestSessionsRoot(t *testing.T) {
	t.Setenv("CLAUDE_HOME", "/home/u/.claude")
	t.Setenv("CODEX_HOME", "/home/u/.codex")
	t.Setenv("OPENCODE_HOME", "/home/u/.local/share/opencode")

	tests := []struct {
		harness, want string
	}{
		{"claude", "/home/u/.claude/projects"},
		{"codex", "/home/u/.codex/sessions"},
		{"opencode", "/home/u/.local/share/opencode/storage/session"},
	}
	for _, tt := range tests {
		got, err := SessionsRoot(tt.harness)
		if err != nil {
			t.Errorf("SessionsRoot(%q): %v", tt.harness, err)
			continue
		}
		if got != tt.want {
			t.Errorf("SessionsRoot(%q) = %q, want %q", tt.harness, got, tt.want)
		}
	}
}
