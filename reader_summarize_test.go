package cc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/ccgit"
)

func TestSummarizeTracksLatestAndDistinctCWDs(t *testing.T) {
	wd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	now := time.Unix(0, 0)
	entries := []Entry{
		{SessionID: "s1", CWD: "/a", Timestamp: now},
		{CWD: "/b", Timestamp: now},
		{CWD: "/a", Timestamp: now},
		{CWD: wd, Timestamp: now},
	}
	sum := Summarize("file.jsonl", entries)
	if sum.CWD != wd {
		t.Errorf("CWD = %q, want %q", sum.CWD, wd)
	}
	want := []string{"/a", "/b", wd}
	if len(sum.DistinctCWDs) != len(want) {
		t.Fatalf("DistinctCWDs = %v, want %v", sum.DistinctCWDs, want)
	}
	for i, v := range want {
		if sum.DistinctCWDs[i] != v {
			t.Errorf("DistinctCWDs[%d] = %q, want %q", i, sum.DistinctCWDs[i], v)
		}
	}
	if sum.GitContext != (ccgit.GitContext{}) {
		t.Errorf("unexpected git context: %+v", sum.GitContext)
	}
}

func TestSummarizeSkipsHarnessPreamble(t *testing.T) {
	entries := []Entry{
		{Message: &Message{Role: "user", Content: json.RawMessage(`"# Project.md instructions\nUse tabs."`)}},
		{Message: &Message{Role: "user", Content: json.RawMessage(`"fix the session title"`)}},
	}
	if got := Summarize("session.jsonl", entries).FirstPrompt; got != "fix the session title" {
		t.Errorf("FirstPrompt = %q, want real user prompt", got)
	}
}

// TestSummaryGitContextJSON guards the wire format: the embedded ccgit.GitContext
// must still flatten its fields into the top level of SessionSummary JSON after
// the move to a separate package.
func TestSummaryGitContextJSON(t *testing.T) {
	sum := SessionSummary{
		SessionID: "s1",
		GitContext: ccgit.GitContext{
			WorktreePath: "/work",
			GitCommonDir: "/work/.git",
			Branch:       "main",
		},
	}
	data, err := json.Marshal(sum)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"worktree_path":"/work"`, `"git_common_dir":"/work/.git"`, `"branch":"main"`} {
		if !strings.Contains(string(data), key) {
			t.Errorf("SessionSummary JSON missing flattened %s\ngot: %s", key, data)
		}
	}
	var back SessionSummary
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.WorktreePath != "/work" || back.Branch != "main" {
		t.Errorf("round-trip lost git context: %+v", back.GitContext)
	}
}

func TestSummarizeDoesNotRunGit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	script := "#!/bin/sh\necho called > '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "session.jsonl")
	entry := Entry{Type: "user", CWD: dir, GitBranch: "recorded", Message: &Message{Role: "user", Content: json.RawMessage(`"hello"`)}}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		summarize func() (SessionSummary, error)
	}{
		{"entries", func() (SessionSummary, error) { return Summarize(path, []Entry{entry}), nil }},
		{"file", func() (SessionSummary, error) { return SummarizeFile(context.Background(), path) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sum, err := tt.summarize()
			if err != nil {
				t.Fatal(err)
			}
			if sum.GitContext != (ccgit.GitContext{}) || sum.GitBranch != "recorded" || sum.FirstPrompt != "hello" {
				t.Fatalf("summary = %+v", sum)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("git ran: stat marker: %v", err)
			}
		})
	}
}
