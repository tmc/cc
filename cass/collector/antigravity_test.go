package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
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

func TestAntigravityTitleSkipsWrapperTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "<USER_REQUEST>\nBuild the browser\n</USER_REQUEST>",
			want:  "Build the browser",
		},
		{
			input: "<USER_REQUEST>Build single line</USER_REQUEST>",
			want:  "Build single line",
		},
		{
			input: "<user_information>\nOS: mac\nWorking directory: /path\n</user_information>\n<USER_REQUEST>\nFix the bug\n</USER_REQUEST>",
			want:  "Fix the bug",
		},
	}

	for _, tt := range tests {
		content, _ := json.Marshal([]cc.ContentBlock{{Type: "text", Text: tt.input}})
		entries := []cc.Entry{{Message: &cc.Message{Role: "user", Content: content}}}
		if got := antigravityTitle(entries, "fallback"); got != tt.want {
			t.Errorf("antigravityTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAntigravitySessionIDPrefixing(t *testing.T) {
	c := &Antigravity{}

	sessionDir := filepath.Join(t.TempDir(), "brain-12345")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessionDir, "task.md"), "Task content")

	session, err := c.parseBrainSession(sessionDir)
	if err != nil {
		t.Fatalf("parseBrainSession failed: %v", err)
	}

	if session.ID != "ag-brain-12345" {
		t.Errorf("session ID = %q, want ag-brain-12345", session.ID)
	}
}

func TestAntigravityRootsDiscovery(t *testing.T) {
	c := &Antigravity{}
	roots, err := c.roots()
	if err != nil {
		t.Fatalf("roots() failed: %v", err)
	}
	if len(roots) < 1 {
		t.Fatalf("expected at least 1 root, got %d", len(roots))
	}
	// When ANTIGRAVITY_HOME is unset, should return 2 roots (~/.gemini/antigravity and ~/.gemini/antigravity-cli)
	if os.Getenv("ANTIGRAVITY_HOME") == "" && len(roots) != 2 {
		t.Errorf("expected 2 roots when ANTIGRAVITY_HOME is unset, got %d: %v", len(roots), roots)
	}
}

func TestAntigravitySubagentDiscovery(t *testing.T) {
	brainDir := filepath.Join(t.TempDir(), "brain")
	sessionDir := filepath.Join(brainDir, "parent-session")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	childID := "ee853370-c2b1-4a06-9c54-b8502c263ece"
	childLogDir := filepath.Join(brainDir, childID, ".system_generated", "logs")
	if err := os.MkdirAll(childLogDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subagentJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Child task"}
`
	childTranscript := filepath.Join(childLogDir, "transcript.jsonl")
	writeFile(t, childTranscript, subagentJSONL)

	subagents := `[{"Model":"inherit","Prompt":"Search codebase","Role":"Artifact Verifier","TypeName":"research"}]`
	result := fmt.Sprintf("Created the following subagents:\n{\n  \"conversationId\": %q,\n  \"logAbsoluteUri\": %q,\n  \"workspaceUris\": [\"file:///Volumes/example/go/src/github.com/tmc/cc\"]\n}", childID, "file://"+childTranscript)
	transcriptJSONL := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Start main task"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Spawn subagent","tool_calls":[{"name":"invoke_subagent","args":{"Subagents":%q}}]}
{"step_index":2,"source":"MODEL","type":"INVOKE_SUBAGENT","status":"DONE","created_at":"2026-06-12T03:22:50Z","content":%q}
`, subagents, result)
	writeFile(t, filepath.Join(logDir, "transcript.jsonl"), transcriptJSONL)

	c := &Antigravity{}
	s, err := c.parseBrainSession(sessionDir)
	if err != nil {
		t.Fatalf("parseBrainSession: %v", err)
	}
	if len(s.Subagents) != 1 {
		t.Fatalf("expected 1 subagent run, got %d", len(s.Subagents))
	}

	sub := s.Subagents[0]
	if sub.AgentID != childID {
		t.Errorf("Subagent AgentID = %q, want %s", sub.AgentID, childID)
	}
	if sub.AgentType != "research" {
		t.Errorf("Subagent AgentType = %q, want research", sub.AgentType)
	}
	if sub.Description != "Search codebase" {
		t.Errorf("Subagent Description = %q, want Search codebase", sub.Description)
	}
	if sub.Status != "unknown" {
		t.Errorf("Subagent Status = %q, want unknown", sub.Status)
	}
	if sub.SourcePath != childTranscript {
		t.Errorf("Subagent SourcePath = %q, want %q", sub.SourcePath, childTranscript)
	}
	if sub.Workspace != "/Volumes/example/go/src/github.com/tmc/cc" {
		t.Errorf("Subagent Workspace = %q", sub.Workspace)
	}
	if s.Stats.SubagentSpawns != 1 {
		t.Errorf("Stats.SubagentSpawns = %d, want 1", s.Stats.SubagentSpawns)
	}
}

func TestAntigravityScanEdgeCasesMissingFullAndCorrupted(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "brain", "session-corrupted-files")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(logDir, "transcript.jsonl"), "corrupted content line\n{\"type\":\"USER_INPUT\",\"content\":\"hi\"}\n")

	c := &Antigravity{Root: root}
	out := make(chan cass.Session, 1)
	if err := c.Scan(context.Background(), cass.ScanConfig{}, out); err != nil {
		t.Fatalf("Scan failed: %v", err)
	}

	s := <-out
	if s.ID != "ag-session-corrupted-files" {
		t.Errorf("session ID = %q, want ag-session-corrupted-files", s.ID)
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

func TestAntigravityScanFullOnlyTranscript(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "brain", "full-only", ".system_generated", "logs")
	mkdirAll(t, logs)
	data := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-04-01T10:00:00Z","content":"full only transcript"}`
	writeFile(t, filepath.Join(logs, "transcript_full.jsonl"), data+"\n")
	c := &Antigravity{Root: root}
	out := make(chan cass.Session, 1)
	if err := c.Scan(t.Context(), cass.ScanConfig{}, out); err != nil {
		t.Fatal(err)
	}
	s, ok := <-out
	if !ok || s.ID != "ag-full-only" {
		t.Fatalf("scan yielded %q, %v, want full-only session", s.ID, ok)
	}
}
