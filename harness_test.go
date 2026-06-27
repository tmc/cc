package cc

import (
	"os"
	"path/filepath"
	"testing"
)

// setHarnessHomes points every *_HOME resolver at roots under base so Harness
// classifies synthetic paths deterministically, independent of the real machine.
// AntigravityHome defaults under GeminiHome (~/.gemini/antigravity); we mirror
// that layout (gemini/antigravity) to exercise the gemini-vs-antigravity tier.
func setHarnessHomes(t *testing.T, base string) (claude, gemini, codex, antigravity string) {
	t.Helper()
	claude = filepath.Join(base, "claude")
	gemini = filepath.Join(base, "gemini")
	codex = filepath.Join(base, "codex")
	antigravity = filepath.Join(gemini, "antigravity")
	t.Setenv("CLAUDE_HOME", claude)
	t.Setenv("GEMINI_HOME", gemini)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("ANTIGRAVITY_HOME", antigravity)
	// Park opencode and pi homes somewhere harmless and out of the way so their
	// predicates never accidentally claim a home-tier path.
	t.Setenv("OPENCODE_HOME", filepath.Join(base, "opencode"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(base, "pi", "agent"))
	return claude, gemini, codex, antigravity
}

func TestHarness(t *testing.T) {
	base := t.TempDir()
	claude, gemini, codex, antigravity := setHarnessHomes(t, base)

	// Real antigravity transcript dir for the predicate tier: a directory
	// containing .system_generated/logs/transcript.jsonl satisfies IsSessionPath.
	agSessionDir := filepath.Join(base, "agy-session")
	agLogDir := filepath.Join(agSessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(agLogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agTranscript := filepath.Join(agLogDir, "transcript.jsonl")
	if err := os.WriteFile(agTranscript, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real opencode session file for the predicate tier.
	ocSessionDir := filepath.Join(base, "opencode", "storage", "session")
	if err := os.MkdirAll(ocSessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ocSession := filepath.Join(ocSessionDir, "ses_abc.json")
	if err := os.WriteFile(ocSession, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real pi session file for the predicate tier: ~/.pi/agent/sessions/<proj>/x.jsonl.
	// The "agent" parent of "sessions" makes IsSessionPath short-circuit without
	// a content sniff, mirroring the real ~/.pi/agent/sessions layout.
	piSession := filepath.Join(base, "pi", "agent", "sessions", "proj", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(piSession), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(piSession, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "claude projects",
			path: filepath.Join(claude, "projects", "-some-proj", "abc.jsonl"),
			want: "claude",
		},
		{
			name: "codex sessions",
			path: filepath.Join(codex, "sessions", "2026", "rollout.jsonl"),
			want: "codex",
		},
		{
			name: "gemini projects",
			path: filepath.Join(gemini, "projects", "-some-proj", "abc.jsonl"),
			want: "gemini",
		},
		{
			name: "antigravity via home under gemini",
			path: filepath.Join(antigravity, "brain", "task", "step.jsonl"),
			want: "antigravity",
		},
		{
			name: "antigravity-cli under gemini",
			path: filepath.Join(gemini, "antigravity-cli", "brain", "task", "step.jsonl"),
			want: "antigravity",
		},
		{
			name: "antigravity predicate transcript dir",
			path: agSessionDir,
			want: "antigravity",
		},
		{
			name: "antigravity predicate transcript file",
			path: agTranscript,
			want: "antigravity",
		},
		{
			name: "opencode predicate session file",
			path: ocSession,
			want: "opencode",
		},
		{
			name: "pi predicate session file",
			path: piSession,
			want: "pi",
		},
		{
			name: "gemini home root but outside all subtrees",
			path: filepath.Join(gemini, "loose.jsonl"),
			want: "unknown",
		},
		{
			name: "under no known home",
			path: filepath.Join(base, "elsewhere", "foo.jsonl"),
			want: "unknown",
		},
		{
			name: "sibling dir not under claude",
			path: filepath.Join(base, "claudex", "projects", "x.jsonl"),
			want: "unknown",
		},
		{
			name: "stdin sentinel",
			path: "-",
			want: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Harness(tt.path); got != tt.want {
				t.Errorf("Harness(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestUnder(t *testing.T) {
	tests := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{name: "empty dir never matches", path: "/a/b", dir: "", want: false},
		{name: "equal", path: "/a/b", dir: "/a/b", want: true},
		{name: "child", path: "/a/b/c", dir: "/a/b", want: true},
		{name: "deep child", path: "/a/b/c/d/e", dir: "/a/b", want: true},
		{name: "parent not under child", path: "/a", dir: "/a/b", want: false},
		{name: "sibling prefix not under", path: "/a/bx/c", dir: "/a/b", want: false},
		{name: "unrelated", path: "/x/y", dir: "/a/b", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := under(tt.path, tt.dir); got != tt.want {
				t.Errorf("under(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
			}
		})
	}
}
