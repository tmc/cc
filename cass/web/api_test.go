package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/ccteamcfg"
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

func TestSearchSummaryOmitsDetailPayload(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(100, 0)
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "search-summary-1",
			Agent:      "codex-cli",
			Title:      "Summary search",
			Workspace:  "/work/summary",
			SourcePath: "/tmp/search-summary.jsonl",
			StartedAt:  start,
			EndedAt:    start.Add(time.Minute),
			Messages:   []cass.Message{{Role: "user", Content: "summary search payload"}},
			Goals:      []cass.Goal{{Objective: "trim payload", Status: "active"}},
			Skills: []cass.SkillUse{
				{Name: "imagegen", Kind: "available", Count: 1, Path: "/tmp/imagegen/SKILL.md"},
				{Name: "go-team-history-audit", Kind: "selected", Count: 1, Path: "/tmp/audit/SKILL.md", Evidence: []string{"Skill tool invocation"}},
			},
			Stats: cass.SessionStats{
				WorkflowRuns:      1,
				WorkflowAgentRuns: 1,
			},
			Workflows: []cass.WorkflowRun{{
				RunID:      "wf_summary",
				Name:       "summary-flow",
				AgentCount: 1,
				Agents:     []cass.WorkflowAgent{{ID: "a1", Title: "agent"}},
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

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=summary&summary=true", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var result cass.SearchResult
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(result.Hits))
	}
	hit := result.Hits[0]
	if !hit.SummaryOnly {
		t.Fatalf("SummaryOnly = false, want true")
	}
	if len(hit.Goals) != 0 || len(hit.Workflows) != 0 {
		t.Fatalf("summary hit carried detail payload: goals=%d workflows=%d",
			len(hit.Goals), len(hit.Workflows))
	}
	if len(hit.Skills) != 1 || hit.Skills[0].Name != "go-team-history-audit" || hit.Skills[0].Kind != "selected" {
		t.Fatalf("summary skills = %#v, want selected skill badge", hit.Skills)
	}
	if hit.Skills[0].Path != "" || len(hit.Skills[0].Evidence) != 0 {
		t.Fatalf("summary skill carried detail payload: %#v", hit.Skills[0])
	}
	if hit.GoalCount != 1 || hit.SkillCount != 2 || hit.SelectedSkillCount != 1 || hit.WorkflowCount != 1 || hit.WorkflowAgentCount != 1 {
		t.Fatalf("summary counts lost: %#v", hit)
	}
}

func TestSearchCountFalseSkipsExactTotal(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(100, 0)
	sessions := []cass.Session{
		{ID: "count-false-1", Agent: "codex-cli", Title: "Count false", Workspace: "/work/count", StartedAt: start, EndedAt: start, Messages: []cass.Message{{Role: "user", Content: "countable"}}},
		{ID: "count-false-2", Agent: "codex-cli", Title: "Count false", Workspace: "/work/count", StartedAt: start.Add(time.Second), EndedAt: start.Add(time.Second), Messages: []cass.Message{{Role: "user", Content: "countable"}}},
		{ID: "count-false-3", Agent: "codex-cli", Title: "Count false", Workspace: "/work/count", StartedAt: start.Add(2 * time.Second), EndedAt: start.Add(2 * time.Second), Messages: []cass.Message{{Role: "user", Content: "countable"}}},
	}
	svc, err := service.New(service.Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: sessions}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 3 {
		t.Fatalf("Index = %d, %v; want 3, nil", n, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=countable&summary=true&count=false&limit=2", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var result cass.SearchResult
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(result.Hits))
	}
	if result.TotalCount != 2 || result.TotalCountExact {
		t.Fatalf("count = %d exact=%v, want lower bound 2", result.TotalCount, result.TotalCountExact)
	}
}

func TestTeamDetailUsesSummarySessions(t *testing.T) {
	ctx := context.Background()
	t.Setenv("CC_TEAMS_DIR", t.TempDir())

	if err := ccteamcfg.WriteTeamConfig("web-team", &ccteamcfg.TeamConfig{
		Name:        "web-team",
		Description: "web test team",
		CreatedAt:   100,
		LeadAgentID: "lead",
		Members: []ccteamcfg.TeamMember{
			{AgentID: "lead", Name: "lead", AgentType: "lead", JoinedAt: 100, CWD: "/work/team"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Unix(100, 0)
	var sessions []cass.Session
	for i := 0; i < 51; i++ {
		sessions = append(sessions, cass.Session{
			ID:        "team-summary-" + strconv.Itoa(i),
			Agent:     "codex-cli",
			Title:     "Team summary",
			Workspace: "/work/team",
			TeamName:  "web-team",
			StartedAt: start.Add(time.Duration(i) * time.Second),
			EndedAt:   start.Add(time.Duration(i) * time.Second),
			Messages:  []cass.Message{{Role: "user", Content: "team work"}},
			Goals:     []cass.Goal{{Objective: "full payload should stay out of team detail", Status: "active"}},
			Skills: []cass.SkillUse{
				{Name: "imagegen", Kind: "available", Path: "/tmp/imagegen/SKILL.md"},
				{Name: "history-audit", Kind: "selected", Path: "/tmp/history/SKILL.md"},
			},
			Stats: cass.SessionStats{
				WorkflowRuns:      1,
				WorkflowAgentRuns: 1,
			},
			Workflows: []cass.WorkflowRun{{
				RunID:      "wf-team",
				Name:       "team-workflow",
				AgentCount: 1,
				Agents:     []cass.WorkflowAgent{{ID: "agent-1", Title: "worker"}},
			}},
		})
	}

	svc, err := service.New(service.Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: sessions}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 51 {
		t.Fatalf("Index = %d, %v; want 51, nil", n, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/teams/web-team", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var body struct {
		Sessions cass.SearchResult `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Sessions.Hits) != 50 {
		t.Fatalf("hits = %d, want 50", len(body.Sessions.Hits))
	}
	if body.Sessions.TotalCount != 50 || body.Sessions.TotalCountExact {
		t.Fatalf("count = %d exact=%v, want lower bound 50", body.Sessions.TotalCount, body.Sessions.TotalCountExact)
	}
	hit := body.Sessions.Hits[0]
	if !hit.SummaryOnly {
		t.Fatalf("SummaryOnly = false, want true")
	}
	if len(hit.Goals) != 0 || len(hit.Workflows) != 0 {
		t.Fatalf("team detail carried nested payload: goals=%d workflows=%d", len(hit.Goals), len(hit.Workflows))
	}
	if len(hit.Skills) != 1 || hit.Skills[0].Name != "history-audit" || hit.Skills[0].Path != "" {
		t.Fatalf("summary skills = %#v, want compact selected skill only", hit.Skills)
	}
}

func TestSessionAgentsUsesIndexedSubagentRuns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	start := time.Unix(100, 0)
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(dir, "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "session-agents-1",
			Agent:      "claude-code",
			Title:      "Indexed agents",
			Workspace:  "/work/agents",
			SourcePath: filepath.Join(dir, "session-agents-1.jsonl"),
			StartedAt:  start,
			EndedAt:    start.Add(time.Minute),
			Messages:   []cass.Message{{Role: "user", Content: "spawn agents"}},
			Stats: cass.SessionStats{
				SubagentSpawns: 2,
			},
			Subagents: []cass.SubagentRun{
				{
					AgentID:     "agent-a",
					AgentType:   "reviewer",
					Description: "review the patch",
					Model:       "claude-sonnet",
					StartedAt:   start.Add(10 * time.Second),
					EndedAt:     start.Add(20 * time.Second),
					Status:      "completed",
					TotalTokens: 123,
					ToolUses:    4,
					DurationMs:  10_000,
					EntryCount:  6,
				},
				{
					AgentID:      "agent-acompact",
					IsCompaction: true,
					StartedAt:    start.Add(15 * time.Second),
					EndedAt:      start.Add(16 * time.Second),
				},
				{
					AgentID:     "agent-b",
					AgentType:   "tester",
					Description: "run tests",
					StartedAt:   start.Add(30 * time.Second),
					EndedAt:     start.Add(35 * time.Second),
					Status:      "error",
					TotalTokens: 55,
					ToolUses:    2,
				},
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

	req := httptest.NewRequest(http.MethodGet, "/api/session/session-agents-1/agents", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var runs []cass.SubagentRun
	if err := json.Unmarshal(rr.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 non-compaction runs: %#v", len(runs), runs)
	}
	if runs[0].AgentID != "agent-b" || runs[0].Status != "error" || runs[0].TotalTokens != 55 {
		t.Fatalf("first run = %#v, want newest agent-b error", runs[0])
	}
	if runs[1].AgentID != "agent-a" || runs[1].AgentType != "reviewer" || runs[1].ToolUses != 4 {
		t.Fatalf("second run = %#v, want agent-a reviewer", runs[1])
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

func TestSessionEntriesRecentPageSkipsOldOversizeLine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "session-tail-1.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"user","x":"`)
	b.WriteString(strings.Repeat("x", cc.MaxLineSize))
	b.WriteString("\"}\n")
	b.WriteString(`{"type":"user","timestamp":"2026-05-28T10:02:00Z","uuid":"recent-a","sessionId":"session-tail-1","message":{"role":"user","content":"recent a"}}` + "\n")
	b.WriteString(`{"type":"assistant","timestamp":"2026-05-28T10:03:00Z","uuid":"recent-b","sessionId":"session-tail-1","message":{"role":"assistant","content":"recent b"}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "session-tail-1",
			Agent:      "codex-cli",
			Title:      "Tail pagination",
			Workspace:  "/work/tail",
			SourcePath: path,
			StartedAt:  time.Unix(100, 0),
			EndedAt:    time.Unix(200, 0),
			Messages:   []cass.Message{{Role: "user", Content: "tail"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 1 {
		t.Fatalf("Index = %d, %v; want 1, nil", n, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/session-tail-1?order=desc&limit=1", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var entries []cc.Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(entries) != 1 || entries[0].UUID != "recent-b" {
		t.Fatalf("paged uuids = %v, want [recent-b]", entryUUIDs(entries))
	}
}

func TestSessionEntriesRecentPageMergesSubagents(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "session-merge-1.jsonl")
	lines := strings.Join([]string{
		`{"type":"user","timestamp":"2026-05-28T10:00:00Z","uuid":"parent-old","sessionId":"session-merge-1","message":{"role":"user","content":"parent old"}}`,
		`{"type":"assistant","timestamp":"2026-05-28T10:04:00Z","uuid":"parent-new","sessionId":"session-merge-1","message":{"role":"assistant","content":"parent new"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if err := os.MkdirAll(filepath.Join(subDir, "workflows", "wf_1"), 0o755); err != nil {
		t.Fatal(err)
	}
	subLines := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-05-28T10:02:00Z","uuid":"sub-mid","sessionId":"session-merge-1","message":{"role":"assistant","content":"sub mid"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "agent-sub.jsonl"), []byte(subLines), 0o644); err != nil {
		t.Fatal(err)
	}
	wfLines := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-05-28T10:03:00Z","uuid":"wf-mid","sessionId":"session-merge-1","message":{"role":"assistant","content":"wf mid"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "workflows", "wf_1", "agent-wf.jsonl"), []byte(wfLines), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "session-merge-1",
			Agent:      "codex-cli",
			Title:      "Merge pagination",
			Workspace:  "/work/merge",
			SourcePath: path,
			StartedAt:  time.Unix(100, 0),
			EndedAt:    time.Unix(200, 0),
			Messages:   []cass.Message{{Role: "user", Content: "merge"}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if n, err := svc.Index(ctx, true); err != nil || n != 1 {
		t.Fatalf("Index = %d, %v; want 1, nil", n, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/session/session-merge-1?order=desc&limit=3", nil)
	rr := httptest.NewRecorder()
	New(Config{Service: svc}).Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var entries []cc.Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := entryUUIDs(entries); strings.Join(got, ",") != "parent-new,wf-mid,sub-mid" {
		t.Fatalf("paged uuids = %v, want [parent-new wf-mid sub-mid]", got)
	}
	if entries[1].AgentID != "wf" || !entries[1].IsSidechain || entries[2].AgentID != "sub" || !entries[2].IsSidechain {
		t.Fatalf("sidechain tags = [%q/%v %q/%v], want wf/sub sidechains",
			entries[1].AgentID, entries[1].IsSidechain, entries[2].AgentID, entries[2].IsSidechain)
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
