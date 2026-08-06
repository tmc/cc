package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tmc/cc"
)

func TestReadClaudeWaiting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waiting.jsonl")
	data := "{\"session_id\":\"one\",\"questions\":[\"First?\"],\"waiting\":true}\n" +
		"{\"session_id\":\"two\",\"waiting\":true}\n" +
		"{\"session_id\":\"one\",\"waiting\":false}\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_WAITING_FILE", path)
	got, err := readClaudeWaiting()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["one"]; ok {
		t.Fatalf("cleared session remains: %#v", got)
	}
	if questions, ok := got["two"]; !ok || len(questions) != 0 {
		t.Fatalf("two = %#v, want present with no questions", got)
	}
}

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
