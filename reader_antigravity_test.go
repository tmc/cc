package cc_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func TestReadAntigravityFullOnly(t *testing.T) {
	root := t.TempDir()
	agy := filepath.Join(root, "agy")
	t.Setenv("ANTIGRAVITY_HOME", agy)
	t.Setenv("CLAUDE_HOME", root)
	t.Setenv("CODEX_HOME", root)
	t.Setenv("GEMINI_HOME", root)
	t.Setenv("OPENCODE_HOME", root)
	t.Setenv("PI_CODING_AGENT_DIR", root)
	t.Setenv("CC_NAC_STORE", filepath.Join(root, "absent.db"))

	dir := filepath.Join(agy, "brain", "full-only")
	logs := filepath.Join(dir, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0755); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(logs, "transcript_full.jsonl")
	const step = `{"step_index":0,"source":"USER","type":"USER_INPUT","content":"inspect main.go"}` + "\n"
	if err := os.WriteFile(full, []byte(step), 0600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(dir, stale, stale); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	files, err := cc.FindSessionFiles(ctx, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{dir}) {
		t.Errorf("FindSessionFiles = %q, want [%q]", files, dir)
	}
	for _, path := range []string{dir, full} {
		entries, err := cc.ReadFile(ctx, path)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", path, err)
			continue
		}
		if len(entries) != 2 || entries[0].SessionID != "ag-full-only" || entries[1].Message == nil || entries[1].Message.TextContent() != "inspect main.go" {
			t.Errorf("ReadFile(%q) = %+v", path, entries)
		}
	}
}
