package cc

import (
	"path/filepath"
	"testing"
	"time"
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
