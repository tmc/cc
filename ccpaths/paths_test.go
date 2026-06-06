package ccpaths

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodePath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"absolute", "/Volumes/tmc/go/src/github.com/tmc/cc", "-Volumes-tmc-go-src-github-com-tmc-cc"},
		{"with dot", "/home/user/.config/claude", "-home-user--config-claude"},
		{"empty", "", ""},
		{"root", "/", "-"},
		{"no slash", "name", "name"},
		{"only dots", "a.b.c", "a-b-c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EncodePath(tt.in)
			if got != tt.want {
				t.Errorf("EncodePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecodeSegments(t *testing.T) {
	// Build a directory tree under a dash-free root to avoid ambiguity
	// when DecodeSegments tries multiple separators on each token.
	root := filepath.Join(t.TempDir(), "decode")
	want := filepath.Join(root, "src", "tmc", "cc")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}

	// EncodePath maps "/" and "." to "-"; passing the encoded path's
	// post-root segments and using `root` as the seed lets DecodeSegments
	// recover the original.
	got, ok := DecodeSegments(root, []string{"src", "tmc", "cc"})
	if !ok {
		t.Fatalf("DecodeSegments returned !ok")
	}
	if got != want {
		t.Errorf("DecodeSegments = %q, want %q", got, want)
	}
}

func TestDecodeSegmentsDotPrefix(t *testing.T) {
	// Empty segment from "--" split must consume the next segment with a
	// "." prefix. Build /<root>/.config and verify decoding from ["", "config"].
	root := filepath.Join(t.TempDir(), "decode")
	want := filepath.Join(root, ".config")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := DecodeSegments(root, []string{"", "config"})
	if !ok {
		t.Fatalf("DecodeSegments returned !ok")
	}
	if got != want {
		t.Errorf("DecodeSegments = %q, want %q", got, want)
	}
}

func TestDecodeSegmentsMissing(t *testing.T) {
	if _, ok := DecodeSegments("", []string{"nope-not-here-" + t.Name()}); ok {
		t.Errorf("DecodeSegments returned ok for missing path")
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"16h", 16 * time.Hour},
		{"30m", 30 * time.Minute},
		{"7d", 7 * 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
	}
	for _, tt := range tests {
		got, err := ParseDuration(tt.in)
		if err != nil {
			t.Fatalf("ParseDuration(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestClaudeHomeOverride(t *testing.T) {
	t.Setenv("CLAUDE_HOME", "/custom/claude")
	got, err := ClaudeHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/claude" {
		t.Errorf("ClaudeHome with CLAUDE_HOME = %q, want /custom/claude", got)
	}
}

func TestGeminiHomeOverride(t *testing.T) {
	t.Setenv("GEMINI_HOME", "/custom/gemini")
	got, err := GeminiHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/gemini" {
		t.Errorf("GeminiHome with GEMINI_HOME = %q, want /custom/gemini", got)
	}
}

func TestCodexHomeOverride(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex")
	got, err := CodexHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/codex" {
		t.Errorf("CodexHome with CODEX_HOME = %q, want /custom/codex", got)
	}
}

func TestOpenCodeHomeOverride(t *testing.T) {
	t.Setenv("OPENCODE_HOME", "/custom/opencode")
	got, err := OpenCodeHome()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/opencode" {
		t.Errorf("OpenCodeHome with OPENCODE_HOME = %q, want /custom/opencode", got)
	}
}
