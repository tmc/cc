package cc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestReadIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions-index.json")
	want := &SessionIndex{
		Version:      1,
		OriginalPath: "/repo",
		Entries: []IndexEntry{
			{SessionID: "s1", ProjectPath: "/repo", MessageCount: 3, Created: "2026-04-01T00:00:00Z", Modified: "2026-04-02T00:00:00Z"},
		},
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadIndex(path)
	if err != nil {
		t.Fatalf("ReadIndex: %v", err)
	}
	if got.Version != want.Version || got.OriginalPath != want.OriginalPath {
		t.Errorf("ReadIndex header = %+v, want %+v", got, want)
	}
	if len(got.Entries) != 1 || got.Entries[0].SessionID != "s1" {
		t.Errorf("ReadIndex entries = %v", got.Entries)
	}
}

func TestIndexEntryTimes(t *testing.T) {
	e := IndexEntry{
		Created:  "2026-04-01T12:00:00Z",
		Modified: "2026-04-02T12:00:00Z",
	}
	if got := e.CreatedTime(); got.IsZero() {
		t.Errorf("CreatedTime returned zero")
	}
	if got := e.ModifiedTime(); got.IsZero() {
		t.Errorf("ModifiedTime returned zero")
	}
	if !e.ModifiedTime().After(e.CreatedTime()) {
		t.Errorf("Modified should be after Created")
	}
}

func TestAllIndexEntriesFilters(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_HOME", root)
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini-empty"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode-empty"))

	now := time.Now()
	idx := SessionIndex{
		Version:      1,
		OriginalPath: "/repo/a",
		Entries: []IndexEntry{
			{SessionID: "fresh-a", ProjectPath: "/repo/alpha", Modified: now.Format(time.RFC3339Nano)},
			{SessionID: "old", ProjectPath: "/repo/alpha", Modified: now.Add(-72 * time.Hour).Format(time.RFC3339Nano)},
			{SessionID: "fresh-b", ProjectPath: "/repo/beta", Modified: now.Format(time.RFC3339Nano)},
		},
	}
	dir := filepath.Join(root, "projects", "encoded-repo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sessions-index.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	// since=24h drops the 72h-old entry; project="alpha" drops fresh-b.
	got, err := AllIndexEntries(24*time.Hour, "alpha")
	if err != nil {
		t.Fatalf("AllIndexEntries: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "fresh-a" {
		ids := make([]string, len(got))
		for i, e := range got {
			ids[i] = e.SessionID
		}
		sort.Strings(ids)
		t.Errorf("AllIndexEntries(24h, alpha) ids = %v, want [fresh-a]", ids)
	}

	// No project filter, wide window: returns the two fresh ones (skips old).
	got, err = AllIndexEntries(48*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("AllIndexEntries(48h, \"\") len = %d, want 2", len(got))
	}
}

func TestAllIndexEntriesIncludesOpenCode(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude-empty"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini-empty"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode"))

	sessionPath := filepath.Join(root, "opencode", "storage", "session", "proj", "ses_index_open.json")
	writeIndexFile(t, sessionPath, `{"id":"ses_index_open","directory":"/work/open","time":{"created":4102444800000}}`)
	writeIndexFile(t, filepath.Join(root, "opencode", "storage", "message", "ses_index_open", "msg_user.json"), `{"id":"msg_user","sessionID":"ses_index_open","role":"user","time":{"created":4102444801000}}`)
	writeIndexFile(t, filepath.Join(root, "opencode", "storage", "part", "msg_user", "prt_user.json"), `{"id":"prt_user","sessionID":"ses_index_open","messageID":"msg_user","type":"text","text":"hello index opencode"}`)

	got, err := AllIndexEntries(24*time.Hour, "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("AllIndexEntries len = %d, want 1: %#v", len(got), got)
	}
	if got[0].SessionID != "ses_index_open" || got[0].ProjectPath != "/work/open" || got[0].FirstPrompt != "hello index opencode" {
		t.Fatalf("AllIndexEntries[0] = %#v", got[0])
	}
}

func writeIndexFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
