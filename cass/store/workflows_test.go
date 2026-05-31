package store

import (
	"context"
	"encoding/json"
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
				RunID:       "wf_aaa",
				TaskID:      "task_aaa",
				Name:        "cc-go-team-review",
				Description: "review the branch",
				Status:      "completed",
				Phases: []cass.WorkflowPhase{
					{Title: "Lenses", Detail: "independent perspectives"},
					{Title: "Stress", Detail: "adversarial checks"},
				},
				AgentCount:        3,
				JournalEventCount: 12,
				StartedAt:         start.Add(time.Minute),
				CompletedAt:       start.Add(30 * time.Minute),
				SourcePath:        "/p/workflows/wf_aaa.json",
				TranscriptDir:     "/p/subagents/workflows/wf_aaa",
				Agents: []cass.WorkflowAgent{
					{
						ID:        "agent-1",
						Label:     "lens:architecture",
						Phase:     "Lenses",
						AgentType: "Explore",
						Title:     "Architecture review",
						Status:    "completed",
					},
					{
						ID:        "agent-2",
						Phase:     "Stress",
						AgentType: "Explore",
						Title:     "You are reviewing this workflow transcript. READ THESE FILES FIRST before responding.",
						Status:    "completed",
					},
				},
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
	var phases []cass.WorkflowPhase
	if err := json.Unmarshal([]byte(w.PhasesJSON), &phases); err != nil {
		t.Fatalf("unmarshal phases: %v", err)
	}
	if len(phases) != 2 || phases[0].Title != "Lenses" || phases[1].Title != "Stress" {
		t.Fatalf("phases = %+v", phases)
	}
	var agents []cass.WorkflowAgent
	if err := json.Unmarshal([]byte(w.AgentsJSON), &agents); err != nil {
		t.Fatalf("unmarshal agents: %v", err)
	}
	if len(agents) != 2 || agents[0].Label != "lens:architecture" || agents[0].Phase != "Lenses" ||
		agents[1].Phase != "Stress" || agents[1].AgentType != "Explore" {
		t.Fatalf("agents = %+v", agents)
	}
	hit, err := s.Session(ctx, "sess1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hit.Workflows) != 2 || len(hit.Workflows[0].Phases) != 2 || len(hit.Workflows[0].Agents) != 2 {
		t.Fatalf("folded workflows = %+v", hit.Workflows)
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
	var promptTitleNode cass.GraphNode
	for _, n := range g.Nodes {
		if n.NodeType == cass.NodeTypeWorkflowAgent {
			agentNodes++
			if n.WorkflowRunID != "wf_aaa" {
				t.Errorf("agent node parented to %q, want wf_aaa", n.WorkflowRunID)
			}
			if n.ID == "wf_aaa#agent-2" {
				promptTitleNode = n
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
	if promptTitleNode.ID == "" {
		t.Fatalf("missing workflow agent node for agent-2")
	}
	if promptTitleNode.Title != "Explore 2" || promptTitleNode.Phase != "Stress" || promptTitleNode.AgentType != "Explore" {
		t.Errorf("agent-2 graph node = %+v, want prompt title hidden with phase/type metadata", promptTitleNode)
	}
}

func TestGraphSubagentNodes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	sess := cass.Session{
		ID:        "sub-parent",
		Agent:     "codex-cli",
		Title:     "spawner",
		Workspace: "tmc/cc",
		StartedAt: start,
		EndedAt:   start.Add(time.Hour),
		Subagents: []cass.SubagentRun{
			{
				AgentID:         "child-aaa",
				ParentSessionID: "sub-parent",
				AgentType:       "worker",
				Status:          "completed",
				StartedAt:       start.Add(time.Minute),
				Workspace:       "tmc/cc",
			},
			{
				AgentID:         "child-bbb",
				ParentSessionID: "sub-parent",
				AgentType:       "reviewer",
				Status:          "unknown",
				StartedAt:       start.Add(2 * time.Minute),
				Workspace:       "tmc/cc",
			},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{Workflow: cass.WorkflowCollapsed})
	if err != nil {
		t.Fatal(err)
	}

	var sessionNodes, subNodes, spawnEdges int
	for _, n := range g.Nodes {
		switch n.NodeType {
		case cass.NodeTypeSession:
			sessionNodes++
		case cass.NodeTypeSubagent:
			subNodes++
			if n.ParentSessionID != "sub-parent" {
				t.Errorf("subagent node parented to %q, want sub-parent", n.ParentSessionID)
			}
		}
	}
	for _, l := range g.Links {
		if l.EdgeType == cass.EdgeSubagentSpawn {
			spawnEdges++
			if l.SourceSession != "sub-parent" {
				t.Errorf("subagent edge from %q, want sub-parent", l.SourceSession)
			}
		}
	}
	if sessionNodes != 1 {
		t.Errorf("session nodes = %d, want 1 (a subagent-only parent must still appear)", sessionNodes)
	}
	if subNodes != 2 {
		t.Errorf("subagent nodes = %d, want 2", subNodes)
	}
	if spawnEdges != 2 {
		t.Errorf("subagent_spawn edges = %d, want 2", spawnEdges)
	}

	// node_type filter to subagents only drops the session node.
	g2, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{
		Workflow:  cass.WorkflowCollapsed,
		NodeTypes: []string{cass.NodeTypeSubagent},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range g2.Nodes {
		if n.NodeType != cass.NodeTypeSubagent {
			t.Errorf("node_type filter leaked %q", n.NodeType)
		}
	}
	if len(g2.Nodes) != 2 {
		t.Errorf("filtered nodes = %d, want 2", len(g2.Nodes))
	}
}

func TestGraphCodexSubagentCollapsesPeerSession(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	// A codex parent whose subagent AgentID is a separately-indexed peer session.
	parent := cass.Session{
		ID:        "codex-parent",
		Agent:     "codex-cli",
		Title:     "spawner",
		Workspace: "tmc/cc",
		StartedAt: start,
		EndedAt:   start.Add(time.Hour),
		Subagents: []cass.SubagentRun{{
			AgentID:         "peer-uuid",
			ParentSessionID: "codex-parent",
			AgentType:       "worker",
			Status:          "completed",
			StartedAt:       start.Add(time.Minute),
		}},
	}
	peer := cass.Session{
		ID:        "peer-uuid",
		Agent:     "codex-cli",
		Title:     "the spawned agent",
		Workspace: "tmc/cc",
		StartedAt: start.Add(time.Minute),
		EndedAt:   start.Add(20 * time.Minute),
		// The peer itself spawns nothing, but is a real indexed session.
		Subagents: []cass.SubagentRun{{
			AgentID:         "grandchild",
			ParentSessionID: "peer-uuid",
			Status:          "unknown",
			StartedAt:       start.Add(2 * time.Minute),
		}},
	}
	if err := s.BatchIndex(ctx, []cass.Session{parent, peer}); err != nil {
		t.Fatal(err)
	}

	g, err := s.GraphDataOpts(ctx, time.Time{}, cass.GraphOptions{Workflow: cass.WorkflowCollapsed})
	if err != nil {
		t.Fatal(err)
	}

	// "peer-uuid" must appear exactly once — as a session node, not also a stub.
	var peerNodes, peerSessionNodes, peerSubNodes int
	for _, n := range g.Nodes {
		if n.ID != "peer-uuid" {
			continue
		}
		peerNodes++
		if n.NodeType == cass.NodeTypeSession {
			peerSessionNodes++
		}
		if n.NodeType == cass.NodeTypeSubagent {
			peerSubNodes++
		}
	}
	if peerNodes != 1 || peerSessionNodes != 1 || peerSubNodes != 0 {
		t.Errorf("peer-uuid nodes: total=%d session=%d subagent-stub=%d, want 1/1/0", peerNodes, peerSessionNodes, peerSubNodes)
	}
	// The spawn edge from the parent to the peer must still exist.
	var edge int
	for _, l := range g.Links {
		if l.EdgeType == cass.EdgeSubagentSpawn && l.SourceSession == "codex-parent" && l.TargetSession == "peer-uuid" {
			edge++
		}
	}
	if edge != 1 {
		t.Errorf("parent->peer subagent_spawn edges = %d, want 1", edge)
	}
	// "grandchild" is not an indexed session, so it stays a stub node.
	var grandStub int
	for _, n := range g.Nodes {
		if n.ID == "grandchild" && n.NodeType == cass.NodeTypeSubagent {
			grandStub++
		}
	}
	if grandStub != 1 {
		t.Errorf("grandchild stub nodes = %d, want 1 (non-indexed agent keeps its stub)", grandStub)
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

func TestSearchFoldsWorkflowMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two sessions: one whose only relevance to the query is its workflow name,
	// and a decoy whose messages do not mention it.
	start := time.Unix(1_700_000_000, 0).UTC()
	sessions := []cass.Session{
		{
			ID:        "wf-parent",
			Agent:     "claude-code",
			Title:     "ordinary title",
			Workspace: "tmc/cc",
			StartedAt: start,
			EndedAt:   start.Add(time.Hour),
			Messages:  []cass.Message{{Role: "user", Content: "do some unrelated work"}},
			Workflows: []cass.WorkflowRun{{
				RunID:      "wf_zzz",
				Name:       "ccmagicreview",
				Status:     "completed",
				AgentCount: 5,
				StartedAt:  start.Add(time.Minute),
			}},
		},
		{
			ID:        "decoy",
			Agent:     "claude-code",
			Title:     "decoy",
			Workspace: "tmc/cc",
			StartedAt: start,
			EndedAt:   start.Add(time.Hour),
			Messages:  []cass.Message{{Role: "user", Content: "nothing to see here"}},
		},
	}
	if err := s.BatchIndex(ctx, sessions); err != nil {
		t.Fatal(err)
	}

	// The query term appears only in the workflow name, indexed into content.
	res, err := s.Search(ctx, cass.SearchRequest{Query: "ccmagicreview", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want 1 (parent session): %+v", len(res.Hits), res.Hits)
	}
	h := res.Hits[0]
	if h.SessionID != "wf-parent" {
		t.Fatalf("hit session = %q, want wf-parent", h.SessionID)
	}
	if !h.CollapsedChildren {
		t.Error("expected CollapsedChildren=true when a workflow matched")
	}
	if len(h.MatchedWorkflowIDs) != 1 || h.MatchedWorkflowIDs[0] != "wf_zzz" {
		t.Errorf("matched workflow ids = %v, want [wf_zzz]", h.MatchedWorkflowIDs)
	}
	if h.WorkflowMatchCount != 5 {
		t.Errorf("workflow_match_count = %d, want 5 (agent count)", h.WorkflowMatchCount)
	}
	// The full workflow list is attached regardless of match.
	if len(h.Workflows) != 1 || h.Workflows[0].RunID != "wf_zzz" {
		t.Errorf("hit workflows = %+v, want one wf_zzz", h.Workflows)
	}
}

func TestSearchAttachesWorkflowsWithoutMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{sessionWithWorkflows()}); err != nil {
		t.Fatal(err)
	}
	// No query: list all, workflows attached but nothing bubbled as matched.
	res, err := s.Search(ctx, cass.SearchRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if len(h.Workflows) != 2 {
		t.Errorf("attached workflows = %d, want 2", len(h.Workflows))
	}
	if h.CollapsedChildren || len(h.MatchedWorkflowIDs) != 0 {
		t.Errorf("no query should mean no folded matches: collapsed=%v matched=%v", h.CollapsedChildren, h.MatchedWorkflowIDs)
	}
}

func TestSearchSummarySkipsWorkflowPayload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := sessionWithWorkflows()
	sess.Stats.WorkflowRuns = 2
	sess.Stats.WorkflowAgentRuns = 3
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(ctx, cass.SearchRequest{Limit: 10, SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if !h.SummaryOnly {
		t.Fatalf("SummaryOnly = false, want true")
	}
	if len(h.Workflows) != 0 {
		t.Fatalf("attached workflows = %+v, want none", h.Workflows)
	}
	if h.WorkflowCount != 2 || h.WorkflowAgentCount != 3 {
		t.Fatalf("workflow counts = %d/%d, want 2/3", h.WorkflowCount, h.WorkflowAgentCount)
	}
}

func TestSearchSummaryFoldsWorkflowMatch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	sess := cass.Session{
		ID:        "wf-parent",
		Agent:     "claude-code",
		Title:     "ordinary title",
		Workspace: "tmc/cc",
		StartedAt: start,
		EndedAt:   start.Add(time.Hour),
		Messages:  []cass.Message{{Role: "user", Content: "do some unrelated work"}},
		Stats: cass.SessionStats{
			WorkflowRuns:      1,
			WorkflowAgentRuns: 5,
		},
		Workflows: []cass.WorkflowRun{{
			RunID:      "wf_zzz",
			Name:       "ccmagicreview",
			Status:     "completed",
			AgentCount: 5,
			StartedAt:  start.Add(time.Minute),
		}},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	res, err := s.Search(ctx, cass.SearchRequest{Query: "ccmagicreview", Limit: 10, SummaryOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if len(h.Workflows) != 0 {
		t.Fatalf("attached workflows = %+v, want none", h.Workflows)
	}
	if !h.CollapsedChildren {
		t.Fatal("expected CollapsedChildren=true when a workflow matched")
	}
	if len(h.MatchedWorkflowIDs) != 1 || h.MatchedWorkflowIDs[0] != "wf_zzz" {
		t.Fatalf("matched workflow ids = %v, want [wf_zzz]", h.MatchedWorkflowIDs)
	}
	if h.WorkflowMatchCount != 5 {
		t.Fatalf("workflow_match_count = %d, want 5", h.WorkflowMatchCount)
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
