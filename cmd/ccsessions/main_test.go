package main

import (
	"path/filepath"
	"testing"

	"github.com/tmc/cc"
)

func TestFilterIndexEntries(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, ".codex")
	t.Setenv("CODEX_HOME", codex)
	dir := filepath.Join(root, "work")
	entries := []cc.IndexEntry{
		{FullPath: filepath.Join(codex, "sessions", "rollout.jsonl"), ProjectPath: dir},
		{FullPath: filepath.Join(root, ".claude", "projects", "p", "session.jsonl"), ProjectPath: dir},
	}
	got := filterIndexEntries(entries, dir, "codex")
	if len(got) != 1 || got[0].FullPath != entries[0].FullPath {
		t.Fatalf("filterIndexEntries = %#v, want codex entry", got)
	}
}

func TestSummaryHasDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "work")
	s := cc.SessionSummary{CWD: filepath.Join(dir, "subdir"), DistinctCWDs: []string{dir}}
	if !summaryHasDirectory(s, dir) {
		t.Fatalf("summaryHasDirectory(%q) = false", dir)
	}
}
