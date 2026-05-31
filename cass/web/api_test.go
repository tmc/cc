package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
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

func TestSessionEntriesPagination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "session-page-1.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T10:00:00Z","uuid":"a","sessionId":"session-page-1","message":{"role":"user","content":"first"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T10:01:00Z","uuid":"b","sessionId":"session-page-1","message":{"role":"assistant","content":"second"}}`,
		`{"type":"user","timestamp":"2026-05-28T10:02:00Z","uuid":"c","sessionId":"session-page-1","message":{"role":"user","content":"third"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T10:03:00Z","uuid":"d","sessionId":"session-page-1","message":{"role":"assistant","content":"fourth"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := service.New(service.Config{
		DBPath: filepath.Join(dir, "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "session-page-1",
			Agent:      "claude-code",
			Title:      "Paged session",
			Workspace:  "/work/page",
			SourcePath: path,
			StartedAt:  time.Unix(100, 0),
			EndedAt:    time.Unix(200, 0),
			Messages:   []cass.Message{{Role: "user", Content: "first"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 1 {
		t.Fatalf("Index = %d, %v; want 1, nil", n, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/session-page-1?order=desc&limit=2&offset=1", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var entries []cc.Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(entries) != 2 || entries[0].UUID != "c" || entries[1].UUID != "b" {
		t.Fatalf("paged uuids = %v, want [c b]", entryUUIDs(entries))
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

func entryUUIDs(entries []cc.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.UUID
	}
	return out
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
