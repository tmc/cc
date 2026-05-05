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
