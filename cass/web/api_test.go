package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
)

type testCollector struct {
	sessions []cass.Session
}

func (c testCollector) Name() string { return "test" }

func (c testCollector) Detect(context.Context) (*cass.DetectionResult, error) {
	return &cass.DetectionResult{Agent: c.Name(), Found: true, Paths: []string{"test"}}, nil
}

func (c testCollector) Scan(ctx context.Context, _ cass.ScanConfig, out chan<- cass.Session) error {
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

func TestSessionMeta(t *testing.T) {
	ctx := context.Background()
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "session-meta-1",
			Agent:      "codex-cli",
			Title:      "Session meta",
			Workspace:  "/work/meta",
			SourcePath: "/tmp/session-meta.jsonl",
			StartedAt:  time.Unix(100, 0),
			EndedAt:    time.Unix(200, 0),
			Messages: []cass.Message{
				{Role: "user", Content: "inspect session metadata"},
			},
			Goals: []cass.Goal{{
				Objective: "ship meta endpoint",
				Status:    "complete",
				CompletionGates: []cass.GoalGate{
					{Name: "real evidence", Status: "missing"},
				},
			}},
			Stats: cass.SessionStats{
				ToolCalls:     3,
				ToolBreakdown: map[string]int{"exec": 2},
			},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 1 {
		t.Fatalf("Index = %d, %v; want 1, nil", n, err)
	}

	handler := New(Config{Service: svc}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/session/session-meta-1/meta", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var hit cass.Hit
	if err := json.Unmarshal(rr.Body.Bytes(), &hit); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if hit.SessionID != "session-meta-1" || hit.Agent != "codex-cli" || hit.Workspace != "/work/meta" {
		t.Fatalf("hit = %#v", hit)
	}
	if len(hit.Goals) != 1 || hit.Goals[0].EffectiveStatus != "blocked" {
		t.Fatalf("goals = %#v", hit.Goals)
	}
	if hit.ToolBreakdown["exec"] != 2 {
		t.Fatalf("tool breakdown = %#v", hit.ToolBreakdown)
	}
}

func TestGraphWorkflowModes(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(1_700_000_000, 0).UTC()
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "graph-sess-1",
			Agent:      "claude-code",
			Title:      "Workflow graph session",
			Workspace:  "tmc/cc",
			SourcePath: "/tmp/graph-sess.jsonl",
			StartedAt:  start,
			EndedAt:    start.Add(time.Hour),
			Messages:   []cass.Message{{Role: "user", Content: "run the workflow"}},
			Workflows: []cass.WorkflowRun{{
				RunID:      "wf_graph",
				Name:       "cc-go-team-review",
				Status:     "completed",
				AgentCount: 2,
				StartedAt:  start.Add(time.Minute),
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
	handler := New(Config{Service: svc}).Handler()

	get := func(query string) cass.GraphData {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/graph"+query, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
		}
		var g cass.GraphData
		if err := json.Unmarshal(rr.Body.Bytes(), &g); err != nil {
			t.Fatalf("decode graph: %v", err)
		}
		return g
	}

	// Default (collapsed): one session node, one workflow node, no agents.
	g := get("?workflow=collapsed")
	var session, workflow, agent int
	for _, n := range g.Nodes {
		switch n.NodeType {
		case cass.NodeTypeSession:
			session++
		case cass.NodeTypeWorkflow:
			workflow++
		case cass.NodeTypeWorkflowAgent:
			agent++
		}
	}
	if session != 1 || workflow != 1 || agent != 0 {
		t.Fatalf("collapsed nodes: session=%d workflow=%d agent=%d, want 1/1/0", session, workflow, agent)
	}
	var contains int
	for _, l := range g.Links {
		if l.EdgeType == cass.EdgeWorkflowContains {
			contains++
		}
	}
	if contains != 1 {
		t.Fatalf("collapsed workflow_contains edges = %d, want 1", contains)
	}

	// Expanded: 2 agent nodes (AgentCount) with workflow_spawn edges.
	g = get("?workflow=expanded")
	agent = 0
	var spawn int
	for _, n := range g.Nodes {
		if n.NodeType == cass.NodeTypeWorkflowAgent {
			agent++
		}
	}
	for _, l := range g.Links {
		if l.EdgeType == cass.EdgeWorkflowSpawn {
			spawn++
		}
	}
	if agent != 2 || spawn != 2 {
		t.Fatalf("expanded: agents=%d spawn=%d, want 2/2", agent, spawn)
	}
}

func TestSessionMetaNotFound(t *testing.T) {
	svc, err := service.New(service.Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	req := httptest.NewRequest(http.MethodGet, "/api/session/missing/meta", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body %q", rr.Code, rr.Body.String())
	}
}
