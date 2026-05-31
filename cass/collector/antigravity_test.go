package collector

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestAntigravityNameAndDetect(t *testing.T) {
	c := &Antigravity{Root: filepath.Join(t.TempDir(), "missing")}
	if got := c.Name(); got != "antigravity" {
		t.Errorf("Name = %q, want antigravity", got)
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

func TestAntigravityScanBrainArtifacts(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "brain", "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	rel, err := filepath.Rel(sessionDir, workspace)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessionDir, "cass.code-workspace"), `{"folders":[{"path":`+quoteJSON(rel)+`}]}`)
	writeFile(t, filepath.Join(sessionDir, "task.md"), "# Build the browser\n\nIndex Antigravity artifacts.")
	writeFile(t, filepath.Join(sessionDir, "task.md.metadata.json"), `{"artifactType":"ARTIFACT_TYPE_TASK","updatedAt":"2026-04-01T12:00:00Z","version":"2","summary":"task summary"}`)
	writeFile(t, filepath.Join(sessionDir, "implementation_plan.md"), "Plan the work.")
	writeFile(t, filepath.Join(sessionDir, "implementation_plan.md.metadata.json"), `{"artifactType":"ARTIFACT_TYPE_IMPLEMENTATION_PLAN","updatedAt":"2026-04-01T12:05:00Z","version":"1"}`)
	writeFile(t, filepath.Join(sessionDir, "task.md.resolved.0"), "old copy")
	writeFile(t, filepath.Join(sessionDir, "screenshot.png"), "not indexed")

	c := &Antigravity{Root: root}
	out := make(chan cass.Session, 2)
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
	if s.ID != "ag-session-1" {
		t.Errorf("ID = %q, want ag-session-1", s.ID)
	}
	if s.Agent != "antigravity" {
		t.Errorf("Agent = %q, want antigravity", s.Agent)
	}
	if s.SourcePath != sessionDir {
		t.Errorf("SourcePath = %q, want %q", s.SourcePath, sessionDir)
	}
	if s.Workspace != workspace {
		t.Errorf("Workspace = %q, want %q", s.Workspace, workspace)
	}
	if s.Title != "Build the browser" {
		t.Errorf("Title = %q, want Build the browser", s.Title)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(s.Messages))
	}
	if s.Messages[0].Role != "user" || !strings.Contains(s.Messages[0].Content, "Index Antigravity") {
		t.Errorf("first message = %+v", s.Messages[0])
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Content != "Plan the work." {
		t.Errorf("second message = %+v", s.Messages[1])
	}
	if s.Stats.Turns != 1 {
		t.Errorf("Stats.Turns = %d, want 1", s.Stats.Turns)
	}
	if s.Stats.DurationSecs != 300 {
		t.Errorf("Stats.DurationSecs = %d, want 300", s.Stats.DurationSecs)
	}
	if s.Metadata["brain_id"] != "session-1" {
		t.Errorf("Metadata.brain_id = %v, want session-1", s.Metadata["brain_id"])
	}
	if s.Metadata["artifact_count"] != 2 {
		t.Errorf("Metadata.artifact_count = %v, want 2", s.Metadata["artifact_count"])
	}
}

func TestAntigravityScanSinceFilter(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "brain", "old")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessionDir, "task.md"), "old task")
	writeFile(t, filepath.Join(sessionDir, "task.md.metadata.json"), `{"updatedAt":"2020-01-01T00:00:00Z"}`)

	c := &Antigravity{Root: root}
	out := make(chan cass.Session, 1)
	cfg := cass.ScanConfig{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := c.Scan(context.Background(), cfg, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	if len(got) != 0 {
		t.Errorf("Scan with Since returned %d sessions, want 0", len(got))
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func quoteJSON(s string) string {
	return strconv.Quote(s)
}
