package cc_test

import (
	"context"
	"fmt"
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

func TestAntigravityListingRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("CLAUDE_HOME", root)
	t.Setenv("CODEX_HOME", root)
	t.Setenv("GEMINI_HOME", root)
	t.Setenv("OPENCODE_HOME", root)
	t.Setenv("PI_CODING_AGENT_DIR", root)
	t.Setenv("CC_NAC_STORE", filepath.Join(root, "absent.db"))

	cli := filepath.Join(root, "antigravity-cli")
	desktop := filepath.Join(root, "antigravity")
	now := time.Now()
	for _, home := range []string{cli, desktop} {
		dir := filepath.Join(home, "brain", filepath.Base(home))
		logs := filepath.Join(dir, ".system_generated", "logs")
		if err := os.MkdirAll(logs, 0755); err != nil {
			t.Fatal(err)
		}
		step := fmt.Sprintf(`{"step_index":0,"source":"USER","type":"USER_INPUT","created_at":%q,"content":"inspect main.go"}`+"\n", now.Format(time.RFC3339Nano))
		if err := os.WriteFile(filepath.Join(logs, "transcript_full.jsonl"), []byte(step), 0600); err != nil {
			t.Fatal(err)
		}
		if home == desktop {
			stale := now.Add(-24 * time.Hour)
			if err := os.Chtimes(dir, stale, stale); err != nil {
				t.Fatal(err)
			}
		}
	}

	tests := []struct {
		name string
		home string
		want int
	}{
		{"same roots", cli, 1},
		{"distinct roots", desktop, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ANTIGRAVITY_HOME", tt.home)
			files, err := cc.FindSessionFiles(context.Background(), time.Hour, "")
			if err != nil || len(files) != tt.want {
				t.Errorf("FindSessionFiles = %q, %v, want %d files", files, err, tt.want)
			}
			entries, err := cc.AllIndexEntries(time.Hour, "")
			if err != nil || len(entries) != tt.want {
				t.Errorf("AllIndexEntries = %v, %v, want %d entries", entries, err, tt.want)
			}
		})
	}
}
