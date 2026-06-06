package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestOpenCodeNameAndDetect(t *testing.T) {
	c := &OpenCode{Root: filepath.Join(t.TempDir(), "missing")}
	if got := c.Name(); got != "opencode" {
		t.Errorf("Name = %q, want opencode", got)
	}
	res, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect missing root: %v", err)
	}
	if res.Found {
		t.Errorf("Detect missing root: Found = true, want false")
	}

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.Root = root
	res, err = c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect existing root: %v", err)
	}
	if !res.Found || len(res.Paths) != 1 || res.Paths[0] != root {
		t.Errorf("Detect existing root = %+v", res)
	}
}

func TestOpenCodeScan(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "session", "proj1", "ses_test.json")
	mkdirAll(t, filepath.Dir(sessionPath))
	writeFile(t, sessionPath, `{
  "id": "ses_test",
  "slug": "quiet-star",
  "version": "0.0.0-dev-202601242139",
  "projectID": "proj1",
  "directory": "/work/repo",
  "parentID": "ses_parent",
  "title": "Build opencode collector",
  "time": {"created": 1780000000000, "updated": 1780000005000},
  "summary": {"additions": 3, "deletions": 1, "files": 2}
}`)
	mkdirAll(t, filepath.Join(root, "message", "ses_test"))
	writeFile(t, filepath.Join(root, "message", "ses_test", "msg_user.json"), `{
  "id": "msg_user",
  "sessionID": "ses_test",
  "role": "user",
  "time": {"created": 1780000001000},
  "summary": {"title": "add opencode support"},
  "agent": "build",
  "model": {"providerID": "openrouter", "modelID": "anthropic/claude-sonnet-4.5"}
}`)
	writeFile(t, filepath.Join(root, "message", "ses_test", "msg_assistant.json"), `{
  "id": "msg_assistant",
  "sessionID": "ses_test",
  "role": "assistant",
  "time": {"created": 1780000002000, "completed": 1780000004000},
  "modelID": "anthropic/claude-sonnet-4.5",
  "providerID": "openrouter",
  "mode": "build",
  "agent": "build",
  "cost": 0.25,
  "tokens": {"input": 10, "output": 5, "reasoning": 1, "cache": {"read": 7, "write": 2}}
}`)
	mkdirAll(t, filepath.Join(root, "part", "msg_assistant"))
	writeFile(t, filepath.Join(root, "part", "msg_assistant", "prt_text.json"), `{
  "id": "prt_text",
  "sessionID": "ses_test",
  "messageID": "msg_assistant",
  "type": "text",
  "text": "done"
}`)
	writeFile(t, filepath.Join(root, "part", "msg_assistant", "prt_tool.json"), `{
  "id": "prt_tool",
  "sessionID": "ses_test",
  "messageID": "msg_assistant",
  "type": "tool",
  "tool": "read",
  "state": {"status": "completed"}
}`)

	c := &OpenCode{Root: root}
	out := make(chan cass.Session, 4)
	if err := c.Scan(context.Background(), cass.ScanConfig{}, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	if len(got) != 1 {
		t.Fatalf("Scan returned %d sessions, want 1", len(got))
	}
	s := got[0]
	if s.ID != "ses_test" {
		t.Errorf("ID = %q, want ses_test", s.ID)
	}
	if s.Agent != "opencode" {
		t.Errorf("Agent = %q, want opencode", s.Agent)
	}
	if s.Workspace != "/work/repo" {
		t.Errorf("Workspace = %q, want /work/repo", s.Workspace)
	}
	if s.Title != "Build opencode collector" {
		t.Errorf("Title = %q, want Build opencode collector", s.Title)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(s.Messages))
	}
	if s.Messages[0].Content != "add opencode support" {
		t.Errorf("first message content = %q", s.Messages[0].Content)
	}
	if s.Messages[1].Content != "done" {
		t.Errorf("assistant content = %q", s.Messages[1].Content)
	}
	if s.Stats.Turns != 1 {
		t.Errorf("Stats.Turns = %d, want 1", s.Stats.Turns)
	}
	if s.Stats.ToolCalls != 1 || s.Stats.ToolBreakdown["read"] != 1 {
		t.Errorf("tool stats = %+v", s.Stats)
	}
	if s.Stats.InputTokens != 10 || s.Stats.OutputTokensSnapshot != 5 || s.Stats.CacheReads != 7 {
		t.Errorf("token stats = %+v", s.Stats)
	}
	if s.Metadata["parent_id"] != "ses_parent" {
		t.Errorf("Metadata.parent_id = %v, want ses_parent", s.Metadata["parent_id"])
	}
	if s.Metadata["version"] != "0.0.0-dev-202601242139" {
		t.Errorf("Metadata.version = %v", s.Metadata["version"])
	}
}

func TestOpenCodeScanSinceFilter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session", "proj", "ses_old.json")
	mkdirAll(t, filepath.Dir(path))
	writeFile(t, path, `{"id":"ses_old","directory":"/work","time":{"created":1780000000000}}`)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	c := &OpenCode{Root: root}
	out := make(chan cass.Session, 1)
	cfg := cass.ScanConfig{Since: time.Now().Add(-24 * time.Hour)}
	if err := c.Scan(context.Background(), cfg, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	if len(got) != 0 {
		t.Errorf("Scan with Since=24h ago returned %d sessions, want 0", len(got))
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
