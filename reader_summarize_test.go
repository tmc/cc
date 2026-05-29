package cc

import (
	"encoding/json"
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
	if sum.WorktreePath == "" {
		t.Errorf("WorktreePath empty for resolvable CWD %q", wd)
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
