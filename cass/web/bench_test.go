package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
)

// benchHandler seeds a store with n synthetic sessions and returns the HTTP
// handler. It measures the full served path (param parsing + query + JSON
// encode), corroborating that endpoint latency is not the cause of the web
// UI's request pile-up (which is client-side fan-out) and guarding regressions.
func benchHandler(b *testing.B, n int) http.Handler {
	b.Helper()
	sessions := make([]cass.Session, n)
	for i := range sessions {
		sessions[i] = cass.Session{
			ID:         fmt.Sprintf("bench-%05d", i),
			Agent:      "claude-code",
			Title:      fmt.Sprintf("Session %d: refactor authentication middleware", i),
			Workspace:  "/home/user/projects/app",
			SourcePath: fmt.Sprintf("/tmp/bench-%05d.jsonl", i),
			StartedAt:  time.Now().Add(-time.Duration(n-i) * time.Minute),
			EndedAt:    time.Now().Add(-time.Duration(n-i-1) * time.Minute),
			Messages: []cass.Message{
				{Role: "user", Content: "investigate the authentication middleware and tests"},
				{Role: "assistant", Content: "found the handler and refactored the token check"},
			},
			Stats: cass.SessionStats{
				ToolCalls: 42, InputTokens: 8000, OutputTokensSnapshot: 2000, Turns: 5,
				WorkflowRuns: 2, WorkflowAgentRuns: 7, WorkflowTaskOps: 3,
			},
		}
	}
	svc, err := service.New(service.Config{
		DBPath:     filepath.Join(b.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: sessions}},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { svc.Close() })
	if _, err := svc.Index(context.Background(), true); err != nil {
		b.Fatal(err)
	}
	return New(Config{Service: svc}).Handler()
}

func benchGET(b *testing.B, handler http.Handler, target string) {
	b.Helper()
	b.ResetTimer()
	for range b.N {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, target, nil))
		if rr.Code != http.StatusOK {
			b.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
		}
	}
}

func BenchmarkHandleSearch(b *testing.B) {
	benchGET(b, benchHandler(b, 500), "/api/search?q=authentication&limit=30")
}

func BenchmarkHandleStats(b *testing.B) {
	benchGET(b, benchHandler(b, 500), "/api/stats?aggregate=true")
}

func BenchmarkHandleGraph(b *testing.B) {
	benchGET(b, benchHandler(b, 500), "/api/graph?workflow=collapsed")
}
