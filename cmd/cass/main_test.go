package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/cass/store"
)

func TestResumeCommandQuotesWorkspace(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid-123",
		Agent:     "codex-cli",
		Workspace: "/Volumes/My Disk/proj",
	})
	want := "cd '/Volumes/My Disk/proj' && codex resume sid-123"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandQuotesShellMetachars(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid 123",
		Agent:     "codex-app",
		Workspace: "/work/proj's; rm -rf /",
	})
	want := "cd '/work/proj'\\''s; rm -rf /' && codex resume 'sid 123'"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandLeavesSafeWorkspaceBare(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID: "sid-123",
		Agent:     "codex-cli",
		Workspace: "/work/proj",
	})
	want := "cd /work/proj && codex resume sid-123"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandUsesClaudeSourcePathID(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID:  "cass-sha-id",
		Agent:      "claude-code",
		Workspace:  "/work/proj",
		SourcePath: "/Users/me/.claude/projects/-work-proj/11111111-2222-3333-4444-555555555555.jsonl",
	})
	want := "cd /work/proj && claude --resume 11111111-2222-3333-4444-555555555555"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestResumeCommandSkipsClaudeSubagentPath(t *testing.T) {
	got := resumeCommand(cass.Hit{
		SessionID:  "cass-sha-id",
		Agent:      "claude-code",
		Workspace:  "/work/proj",
		SourcePath: "/Users/me/.claude/projects/-work-proj/11111111/subagents/agent-worker.jsonl",
	})
	want := "cd /work/proj && claude --resume"
	if got != want {
		t.Fatalf("resumeCommand = %q, want %q", got, want)
	}
}

func TestWorkflowEntryFromRowDecodesPhasesAndAgents(t *testing.T) {
	got := workflowEntryFromRow(store.WorkflowRow{
		ParentSessionID: "parent-1",
		RunID:           "wf_1",
		Name:            "review workflow",
		Status:          "completed",
		AgentCount:      2,
		PhasesJSON:      `[{"title":"Review","detail":"lens pass"}]`,
		AgentsJSON:      `[{"id":"agent-a","label":"lens:api","phase":"Review","agent_type":"Explore"}]`,
	})
	if got.ParentSessionID != "parent-1" || got.RunID != "wf_1" || got.Name != "review workflow" {
		t.Fatalf("workflow entry identity = %+v", got)
	}
	if len(got.Phases) != 1 || got.Phases[0].Title != "Review" || got.Phases[0].Detail != "lens pass" {
		t.Fatalf("phases = %+v", got.Phases)
	}
	if len(got.Agents) != 1 || got.Agents[0].Label != "lens:api" ||
		got.Agents[0].Phase != "Review" || got.Agents[0].AgentType != "Explore" {
		t.Fatalf("agents = %+v", got.Agents)
	}
}

func TestWorkflowAgentLabelsSkipPromptTitles(t *testing.T) {
	got := workflowAgentLabels([]cass.WorkflowAgent{{
		ID:        "agent-a",
		Title:     "You are reviewing this workflow transcript. READ THESE FILES FIRST before responding.",
		AgentType: "Explore",
	}})
	if len(got) != 1 || got[0] != "Explore" {
		t.Fatalf("workflowAgentLabels = %v, want [Explore]", got)
	}
}

func TestRunWorkflowsListsIndexedRuns(t *testing.T) {
	ctx := context.Background()
	start := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{cassTestCollector{sessions: []cass.Session{{
			ID:        "parent-1",
			Agent:     "claude-code",
			Title:     "workflow parent",
			Workspace: "/work/proj",
			StartedAt: start,
			EndedAt:   start.Add(time.Hour),
			Messages:  []cass.Message{{Role: "user", Content: "run workflow"}},
			Workflows: []cass.WorkflowRun{{
				RunID:       "wf_1",
				Name:        "review workflow",
				Description: "review this branch",
				Status:      "completed",
				Phases: []cass.WorkflowPhase{
					{Title: "Review"},
					{Title: "Synthesize"},
				},
				AgentCount:        2,
				JournalEventCount: 7,
				StartedAt:         start.Add(time.Minute),
				Agents: []cass.WorkflowAgent{
					{ID: "agent-a", Label: "lens:api", Phase: "Review", AgentType: "Explore"},
					{ID: "agent-b", Phase: "Synthesize", AgentType: "Writer"},
				},
			}, {
				RunID:     "wf_old",
				Name:      "old workflow",
				Status:    "completed",
				StartedAt: time.Now().Add(-48 * time.Hour),
			}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 1 {
		t.Fatalf("Index = %d, %v; want 1, nil", n, err)
	}

	text, err := captureStdout(t, func() error {
		return runWorkflows(ctx, svc, []string{"--session", "parent-1"}, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"review workflow",
		"completed",
		"phases: Review / Synthesize",
		"agents: lens:api, Writer",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("workflow text output missing %q:\n%s", want, text)
		}
	}

	raw, err := captureStdout(t, func() error {
		return runWorkflows(ctx, svc, []string{"parent-1"}, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []workflowListEntry
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, raw)
	}
	recent := workflowByRunID(got, "wf_1")
	if len(got) != 2 || recent == nil || len(recent.Phases) != 2 || len(recent.Agents) != 2 {
		t.Fatalf("workflow JSON output = %+v", got)
	}
	if recent.CompletedAt != 0 {
		t.Fatalf("workflow completed_at = %d, want omitted zero value", recent.CompletedAt)
	}

	raw, err = captureStdout(t, func() error {
		return runWorkflows(ctx, svc, []string{"--since", "24h"}, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode recent JSON output: %v\n%s", err, raw)
	}
	if len(got) != 1 || got[0].RunID != "wf_1" {
		t.Fatalf("recent workflow JSON output = %+v, want only wf_1", got)
	}
}

func workflowByRunID(workflows []workflowListEntry, id string) *workflowListEntry {
	for i := range workflows {
		if workflows[i].RunID == id {
			return &workflows[i]
		}
	}
	return nil
}

type cassTestCollector struct {
	sessions []cass.Session
}

func (c cassTestCollector) Name() string { return "test" }

func (c cassTestCollector) Detect(context.Context) (*cass.DetectionResult, error) {
	return &cass.DetectionResult{Agent: c.Name(), Found: true, Paths: []string{"test"}}, nil
}

func (c cassTestCollector) Scan(ctx context.Context, _ cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	for _, sess := range c.sessions {
		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct {
		b   []byte
		err error
	}, 1)
	go func() {
		b, err := io.ReadAll(r)
		readDone <- struct {
			b   []byte
			err error
		}{b: b, err: err}
	}()
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old
	read := <-readDone
	r.Close()
	if runErr != nil {
		return string(read.b), runErr
	}
	return string(read.b), read.err
}
