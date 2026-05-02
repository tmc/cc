package collector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestOpenClawNameAndDetect(t *testing.T) {
	c := &OpenClaw{Root: filepath.Join(t.TempDir(), "missing")}
	if got := c.Name(); got != "openclaw" {
		t.Errorf("Name = %q, want %q", got, "openclaw")
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

func TestOpenClawScan(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent-1", "sessions")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}

	jsonl := strings.Join([]string{
		`{"type":"session","id":"sess-1","timestamp":"2026-04-01T12:00:00Z","cwd":"/work/repo"}`,
		`{"type":"model_change","timestamp":"2026-04-01T12:00:01Z","modelId":"claude-sonnet-4-6"}`,
		`{"type":"message","id":"m1","timestamp":"2026-04-01T12:00:02Z","message":{"role":"user","content":[{"type":"text","text":"build the parser"}]}}`,
		`{"type":"message","id":"m2","timestamp":"2026-04-01T12:00:05Z","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
		`{"type":"message","id":"m3","timestamp":"2026-04-01T12:00:10Z","message":{"role":"user","content":[{"type":"text","text":"thanks"}]}}`,
	}, "\n")
	if err := os.WriteFile(filepath.Join(agent, "session-1.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &OpenClaw{Root: root}
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
	if s.ID != "sess-1" {
		t.Errorf("ID = %q, want sess-1 (canonical from session event)", s.ID)
	}
	if s.Workspace != "/work/repo" {
		t.Errorf("Workspace = %q, want /work/repo", s.Workspace)
	}
	if s.Agent != "openclaw" {
		t.Errorf("Agent = %q, want openclaw", s.Agent)
	}
	if len(s.Messages) != 3 {
		t.Errorf("Messages = %d, want 3", len(s.Messages))
	}
	if s.Stats.Turns != 2 {
		t.Errorf("Stats.Turns = %d, want 2 (two user messages)", s.Stats.Turns)
	}
	if s.Title != "build the parser" {
		t.Errorf("Title = %q, want %q", s.Title, "build the parser")
	}
	if s.Metadata["agent_id"] != "agent-1" {
		t.Errorf("Metadata.agent_id = %v, want agent-1", s.Metadata["agent_id"])
	}
	if s.Metadata["model"] != "claude-sonnet-4-6" {
		t.Errorf("Metadata.model = %v, want claude-sonnet-4-6", s.Metadata["model"])
	}
}

func TestOpenClawScanSinceFilter(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "a", "sessions")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(agent, "old.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"session","id":"s","timestamp":"2020-01-01T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	c := &OpenClaw{Root: root}
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
