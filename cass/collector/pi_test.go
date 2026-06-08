package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestPiNameAndDetect(t *testing.T) {
	c := &Pi{Root: filepath.Join(t.TempDir(), "missing")}
	if got := c.Name(); got != "pi" {
		t.Errorf("Name = %q, want pi", got)
	}
	res, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect missing root: %v", err)
	}
	if res.Found {
		t.Errorf("Detect missing root: Found = true, want false")
	}

	root := t.TempDir()
	c.Root = root
	res, err = c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect existing root: %v", err)
	}
	if !res.Found || len(res.Paths) != 1 || res.Paths[0] != root {
		t.Errorf("Detect existing root = %+v", res)
	}
}

func TestPiScan(t *testing.T) {
	// Root mirrors pi's real layout (<PiHome>/sessions) so the walked files sit
	// under a sessions/ dir, which is how cc.ReadFile detects them as pi.
	root := filepath.Join(t.TempDir(), "sessions")
	sessionPath := filepath.Join(root, "--work-repo--", "2026-04-19T18-49-16-013Z_sid.jsonl")
	mkdirAll(t, filepath.Dir(sessionPath))
	writeFile(t, sessionPath, `{"type":"session","version":3,"id":"sid-pi","timestamp":"2026-04-19T18:49:16.013Z","cwd":"/work/repo"}
{"type":"model_change","id":"m1","parentId":null,"timestamp":"2026-04-19T18:49:16.092Z","provider":"anthropic","modelId":"claude-sonnet-4"}
{"type":"message","id":"u1","parentId":"m1","timestamp":"2026-04-19T18:49:31.812Z","message":{"role":"user","content":[{"type":"text","text":"read the readme"}],"timestamp":1}}
{"type":"message","id":"a1","parentId":"u1","timestamp":"2026-04-19T18:49:42.186Z","message":{"role":"assistant","content":[{"type":"text","text":"on it"},{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"README.md"}}],"model":"claude-sonnet-4","usage":{"input":10,"output":5,"cacheRead":7,"cacheWrite":1},"timestamp":2}}
{"type":"message","id":"t1","parentId":"a1","timestamp":"2026-04-19T18:50:01.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":"# readme","isError":false,"timestamp":3}}
`)

	c := &Pi{Root: root}
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
	if s.ID != "sid-pi" {
		t.Errorf("ID = %q, want sid-pi", s.ID)
	}
	if s.Agent != "pi" {
		t.Errorf("Agent = %q, want pi", s.Agent)
	}
	if s.Workspace != "/work/repo" {
		t.Errorf("Workspace = %q, want /work/repo", s.Workspace)
	}
	if s.Title != "read the readme" {
		t.Errorf("Title = %q, want read the readme", s.Title)
	}
	// user prompt + assistant text + tool-result text.
	if len(s.Messages) != 3 {
		t.Fatalf("Messages = %d, want 3: %+v", len(s.Messages), s.Messages)
	}
	if s.Messages[0].Content != "read the readme" {
		t.Errorf("first message content = %q", s.Messages[0].Content)
	}
	if s.Stats.Turns != 1 {
		t.Errorf("Stats.Turns = %d, want 1", s.Stats.Turns)
	}
	if s.Stats.ToolCalls != 1 || s.Stats.ToolBreakdown["Read"] != 1 {
		t.Errorf("tool stats = %+v", s.Stats)
	}
	if s.Stats.InputTokens != 10 || s.Stats.CacheReads != 7 || s.Stats.CacheCreationInputTokens != 1 {
		t.Errorf("token stats = %+v", s.Stats)
	}
	if s.Metadata["model"] != "claude-sonnet-4" || s.Metadata["version"] != "3" {
		t.Errorf("metadata = %+v", s.Metadata)
	}
}

func TestPiScanSinceFilter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "proj", "old.jsonl")
	mkdirAll(t, filepath.Dir(path))
	writeFile(t, path, `{"type":"session","id":"old","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/work"}
{"type":"message","id":"u","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1}}
`)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	c := &Pi{Root: root}
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
