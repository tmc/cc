package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/collector"
)

// TestIndexPathsWorkflowAgentAttributed is the regression guard for the bug
// where a changed workflow-agent file (rooted below subagents/) was indexed as
// a standalone top-level session. After the fix, IndexPaths on the agent path
// must: emit zero standalone rows, fold the agent's text into the parent (so an
// agent-only query surfaces the parent), populate the parent's WorkflowRun
// agents, and set the matched-in-N-agents badge.
func TestIndexPathsWorkflowAgentAttributed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir() // stands in for ~/.claude

	projectDir := filepath.Join(root, "projects", "-work-wf")
	sessionDir := filepath.Join(projectDir, "parentuuid")
	transcriptDir := filepath.Join(sessionDir, "subagents", "workflows", "wf_xyz")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Parent session: launches a Workflow.
	parentPath := filepath.Join(projectDir, "parentuuid.jsonl")
	parentLines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T17:14:00Z","message":{"role":"user","content":"kick off the workflow"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T17:15:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Workflow","input":{"script":"export const meta = { name: 'attr-test', description: 'check attribution' }"}}]}}`,
		`{"type":"user","timestamp":"2026-05-28T17:15:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Workflow launched in background. Task ID: t1\nTranscript dir: ` + transcriptDir + `\nRun ID: wf_xyz","is_error":false}]},"toolUseResult":{"status":"async_launched","taskId":"t1","runId":"wf_xyz","transcriptDir":"` + transcriptDir + `"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(parentPath, []byte(parentLines), 0o644); err != nil {
		t.Fatal(err)
	}

	// Workflow agent transcript with a UNIQUE token only present here.
	agentPath := filepath.Join(transcriptDir, "agent-aone.jsonl")
	agentLines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T17:16:00Z","message":{"role":"user","content":"zorpquux investigate the parser"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T17:16:30Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(agentPath, []byte(agentLines), 0o644); err != nil {
		t.Fatal(err)
	}
	// A journal so the run looks complete.
	os.WriteFile(filepath.Join(transcriptDir, "journal.jsonl"), []byte("{}\n"), 0o644)

	// Service whose ClaudeCode collector is rooted at this temp projects dir.
	cc := &collector.ClaudeCode{Root: filepath.Join(root, "projects")}
	svc, err := New(Config{DBPath: filepath.Join(root, "index.db"), Collectors: []cass.Collector{cc}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	// Reproduce the bug path: IndexPaths with ONLY the changed agent file.
	if _, err := svc.IndexPaths(ctx, []string{agentPath}); err != nil {
		t.Fatalf("IndexPaths(agent): %v", err)
	}

	// (a) zero standalone rows under a subagents segment.
	if n := countSubagentRows(t, svc); n != 0 {
		t.Errorf("standalone subagent session rows = %d, want 0", n)
	}

	// (b) agent-only text surfaces the PARENT, with the match badge.
	res, err := svc.Search(ctx, cass.SearchRequest{Query: "zorpquux", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits for agent-only text = %d, want 1 (the parent)", len(res.Hits))
	}
	h := res.Hits[0]
	if !strings.HasSuffix(h.SourcePath, "parentuuid.jsonl") {
		t.Errorf("hit source_path = %q, want the parent .jsonl", h.SourcePath)
	}
	if !h.CollapsedChildren || h.WorkflowMatchCount == 0 {
		t.Errorf("expected matched-in-N-agents badge: CollapsedChildren=%v WorkflowMatchCount=%d",
			h.CollapsedChildren, h.WorkflowMatchCount)
	}

	// (c) parent's WorkflowRun.Agents populated with the real agent.
	if len(h.Workflows) != 1 {
		t.Fatalf("parent workflows = %d, want 1", len(h.Workflows))
	}
	agents := h.Workflows[0].Agents
	if len(agents) != 1 || agents[0].ID != "aone" {
		t.Fatalf("workflow agents = %+v, want one with id 'aone'", agents)
	}
	if !strings.HasSuffix(agents[0].SourcePath, "agent-aone.jsonl") {
		t.Errorf("agent source_path = %q", agents[0].SourcePath)
	}
}

func TestSessionEnrichesStaleWorkflowMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	scriptPath := filepath.Join(root, "review.js")
	transcriptDir := filepath.Join(root, "subagents", "workflows", "wf_stale")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `
export const meta = {
  name: 'review-flow',
  description: 'review stale metadata',
  phases: [
    { title: 'Lenses', detail: 'first pass' },
  ],
}
phase('Lenses')
await agent('review api surface', { label: 'lens:api', phase: 'Lenses', agentType: 'Explore' })
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(transcriptDir, "agent-aone.jsonl")
	agentLines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T17:16:00Z","message":{"role":"user","content":"review api surface"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T17:16:30Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":{}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(agentPath, []byte(agentLines), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Config{DBPath: filepath.Join(root, "index.db"), Collectors: nil})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	start := mustParseTime(t, "2026-05-28T17:14:00Z")
	stale := cass.Session{
		ID:         "stale-parent",
		Agent:      "claude-code",
		Title:      "<command-name>/effort</command-name><command-message>effort</command-message>",
		Workspace:  "tmc/cc",
		SourcePath: filepath.Join(root, "parent.jsonl"),
		StartedAt:  start,
		EndedAt:    start.Add(time.Minute),
		Messages:   []cass.Message{{Role: "user", Content: "run workflow", CreatedAt: start}},
		Workflows: []cass.WorkflowRun{{
			RunID:         "wf_stale",
			Name:          "review-flow",
			ScriptPath:    scriptPath,
			TranscriptDir: transcriptDir,
			AgentCount:    1,
			StartedAt:     start,
			Agents: []cass.WorkflowAgent{{
				ID:         "aone",
				Title:      "You are reviewing this workflow transcript. READ THESE FILES FIRST before responding.",
				SourcePath: agentPath,
				Status:     "completed",
			}},
		}},
	}
	if err := svc.store.BatchIndex(ctx, []cass.Session{stale}); err != nil {
		t.Fatal(err)
	}

	hit, err := svc.Session(ctx, "stale-parent")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Title != "review-flow" {
		t.Fatalf("session title = %q, want workflow title", hit.Title)
	}
	assertEnrichedWorkflow(t, hit)

	result, err := svc.Search(ctx, cass.SearchRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(result.Hits))
	}
	if result.Hits[0].Title != "review-flow" {
		t.Fatalf("search title = %q, want workflow title", result.Hits[0].Title)
	}
	assertEnrichedWorkflow(t, result.Hits[0])

	summary, err := svc.SearchSummary(ctx, cass.SearchRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Hits) != 1 {
		t.Fatalf("summary hits = %d, want 1", len(summary.Hits))
	}
	if summary.Hits[0].Title != "review-flow" {
		t.Fatalf("summary title = %q, want workflow title", summary.Hits[0].Title)
	}
}

func assertEnrichedWorkflow(t *testing.T, hit cass.Hit) {
	t.Helper()
	if len(hit.Workflows) != 1 {
		t.Fatalf("workflows = %+v, want one", hit.Workflows)
	}
	wf := hit.Workflows[0]
	if len(wf.Phases) != 1 || wf.Phases[0].Title != "Lenses" || wf.Phases[0].Detail != "first pass" {
		t.Fatalf("phases = %+v, want Lenses phase", wf.Phases)
	}
	if len(wf.Agents) != 1 {
		t.Fatalf("agents = %+v, want one", wf.Agents)
	}
	a := wf.Agents[0]
	if a.Label != "lens:api" || a.Phase != "Lenses" || a.AgentType != "Explore" {
		t.Fatalf("agent metadata = %+v, want lens:api/Lenses/Explore", a)
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestParentSessionPath(t *testing.T) {
	cases := []struct{ in, want string }{
		// Nested workflow-agent collapses to the parent uuid .jsonl.
		{"/p/-work/uuid/subagents/workflows/wf_x/agent-a.jsonl", "/p/-work/uuid.jsonl"},
		// Flat subagent collapses the same way.
		{"/p/-work/uuid/subagents/agent-a.jsonl", "/p/-work/uuid.jsonl"},
		// A plain parent session path is unchanged.
		{"/p/-work/uuid.jsonl", "/p/-work/uuid.jsonl"},
		// A project literally containing "subagents" in a name is not a segment.
		{"/p/my-subagents-tool/uuid.jsonl", "/p/my-subagents-tool/uuid.jsonl"},
	}
	for _, c := range cases {
		if got := ParentSessionPath(c.in); got != c.want {
			t.Errorf("ParentSessionPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func countSubagentRows(t *testing.T, svc *Service) int {
	t.Helper()
	// Search broadly and count any hit whose source_path is under a subagents
	// segment — there should be none.
	res, err := svc.Search(context.Background(), cass.SearchRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, h := range res.Hits {
		if strings.Contains(filepath.ToSlash(h.SourcePath), "/subagents/") {
			n++
		}
	}
	return n
}
