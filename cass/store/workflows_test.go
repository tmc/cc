package store

import (
	"context"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

// sessionWithWorkflows returns a session carrying two workflow runs, one of
// which fanned out into child agents.
func sessionWithWorkflows() cass.Session {
	start := time.Unix(1_700_000_000, 0).UTC()
	return cass.Session{
		ID:        "sess1",
		Agent:     "claude-code",
		Title:     "Implement the review action",
		Workspace: "tmc/cc",
		StartedAt: start,
		EndedAt:   start.Add(time.Hour),
		Workflows: []cass.WorkflowRun{
			{
				RunID:             "wf_aaa",
				TaskID:            "task_aaa",
				Name:              "cc-go-team-review",
				Description:       "review the branch",
				Status:            "completed",
				AgentCount:        3,
				JournalEventCount: 12,
				StartedAt:         start.Add(time.Minute),
				CompletedAt:       start.Add(30 * time.Minute),
				SourcePath:        "/p/workflows/wf_aaa.json",
				TranscriptDir:     "/p/subagents/workflows/wf_aaa",
			},
			{
				RunID:      "wf_bbb",
				Name:       "cc-implement",
				Status:     "async_launched",
				AgentCount: 0,
				StartedAt:  start.Add(2 * time.Minute),
			},
		},
	}
}

func TestWorkflowPersistenceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Workflows(ctx, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d workflows, want 2", len(got))
	}
	// Ordered by started_at, then run id: wf_aaa (start+1m) before wf_bbb (start+2m).
	if got[0].RunID != "wf_aaa" || got[1].RunID != "wf_bbb" {
		t.Fatalf("unexpected order: %s, %s", got[0].RunID, got[1].RunID)
	}
	w := got[0]
	if w.Name != "cc-go-team-review" || w.Status != "completed" || w.AgentCount != 3 || w.JournalEventCount != 12 {
		t.Errorf("wf_aaa fields wrong: %+v", w)
	}
	if w.ParentSessionID != "sess1" {
		t.Errorf("parent session = %q, want sess1", w.ParentSessionID)
	}

	n, err := s.WorkflowCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("workflow count = %d, want 2", n)
	}
}

func TestWorkflowReindexReplaces(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := sessionWithWorkflows()
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}
	// Re-index with a single workflow; the stale second run must be cleared.
	sess.Workflows = sess.Workflows[:1]
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Workflows(ctx, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("after reindex got %d workflows, want 1", len(got))
	}
}

func TestGraphCollapsedMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}

	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{Workflow: cass.WorkflowCollapsed})
	if err != nil {
		t.Fatal(err)
	}

	var sessionNodes, workflowNodes, agentNodes int
	var sessionNode cass.GraphNode
	for _, n := range g.Nodes {
		switch n.NodeType {
		case cass.NodeTypeSession:
			sessionNodes++
			sessionNode = n
		case cass.NodeTypeWorkflow:
			workflowNodes++
		case cass.NodeTypeWorkflowAgent:
			agentNodes++
		}
	}
	if sessionNodes != 1 {
		t.Errorf("session nodes = %d, want 1", sessionNodes)
	}
	if workflowNodes != 2 {
		t.Errorf("workflow nodes = %d, want 2", workflowNodes)
	}
	if agentNodes != 0 {
		t.Errorf("collapsed mode should have 0 agent nodes, got %d", agentNodes)
	}
	if sessionNode.WorkflowCount != 2 {
		t.Errorf("session workflow_count = %d, want 2", sessionNode.WorkflowCount)
	}
	// Two workflow_contains edges (session -> each workflow), no spawn edges.
	var contains, spawn int
	for _, l := range g.Links {
		switch l.EdgeType {
		case cass.EdgeWorkflowContains:
			contains++
		case cass.EdgeWorkflowSpawn:
			spawn++
		}
	}
	if contains != 2 {
		t.Errorf("workflow_contains edges = %d, want 2", contains)
	}
	if spawn != 0 {
		t.Errorf("collapsed mode should have 0 spawn edges, got %d", spawn)
	}
}

func TestGraphExpandedMode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}

	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{Workflow: cass.WorkflowExpanded})
	if err != nil {
		t.Fatal(err)
	}

	var agentNodes, spawn int
	for _, n := range g.Nodes {
		if n.NodeType == cass.NodeTypeWorkflowAgent {
			agentNodes++
			if n.WorkflowRunID != "wf_aaa" {
				t.Errorf("agent node parented to %q, want wf_aaa", n.WorkflowRunID)
			}
		}
	}
	for _, l := range g.Links {
		if l.EdgeType == cass.EdgeWorkflowSpawn {
			spawn++
		}
	}
	// wf_aaa has AgentCount 3; wf_bbb has 0.
	if agentNodes != 3 {
		t.Errorf("expanded agent nodes = %d, want 3", agentNodes)
	}
	if spawn != 3 {
		t.Errorf("workflow_spawn edges = %d, want 3", spawn)
	}
}

func TestGraphNodeTypeFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}

	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{
		Workflow:  cass.WorkflowCollapsed,
		NodeTypes: []string{cass.NodeTypeWorkflow},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.NodeType != cass.NodeTypeWorkflow {
			t.Errorf("node_type filter leaked %q node", n.NodeType)
		}
	}
	if len(g.Nodes) != 2 {
		t.Errorf("filtered to %d nodes, want 2 workflow nodes", len(g.Nodes))
	}
}

func TestGraphNoneModeIsLegacy(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}
	// No session links indexed, so the legacy graph is empty of workflow nodes.
	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{Workflow: cass.WorkflowNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g.Nodes {
		if n.NodeType == cass.NodeTypeWorkflow {
			t.Errorf("none mode should not emit workflow nodes, got %+v", n)
		}
	}
}

func TestDeleteCascadesWorkflows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, cass.DeleteFilter{IDs: []string{"sess1"}}); err != nil {
		t.Fatal(err)
	}
	n, err := s.WorkflowCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("after delete, workflow count = %d, want 0", n)
	}
}
