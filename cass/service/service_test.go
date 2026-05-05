package service

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

type collectorFunc struct {
	name     string
	sessions []cass.Session
}

func (c collectorFunc) Name() string { return c.name }

func (c collectorFunc) Detect(context.Context) (*cass.DetectionResult, error) {
	return &cass.DetectionResult{Agent: c.name, Found: true, Paths: []string{c.name}}, nil
}

func (c collectorFunc) Scan(ctx context.Context, _ cass.ScanConfig, out chan<- cass.Session) error {
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

func TestIndexMultipleCollectors(t *testing.T) {
	ctx := context.Background()
	svc, err := New(Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{
			collectorFunc{name: "agent-a", sessions: serviceTestSessions("a", 150)},
			collectorFunc{name: "agent-b", sessions: serviceTestSessions("b", 150)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	n, err := svc.Index(ctx, true)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if n != 300 {
		t.Fatalf("Index = %d, want 300", n)
	}

	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got := stats["session_count"]; got != 300 {
		t.Fatalf("session_count = %v, want 300", got)
	}
}

func serviceTestSessions(prefix string, n int) []cass.Session {
	sessions := make([]cass.Session, n)
	for i := range sessions {
		id := fmt.Sprintf("%s-%03d", prefix, i)
		sessions[i] = cass.Session{
			ID:        id,
			Agent:     "codex-cli",
			Title:     "session " + id,
			Workspace: "/tmp/service-test",
			StartedAt: time.Unix(int64(i), 0),
			EndedAt:   time.Unix(int64(i+1), 0),
			Messages: []cass.Message{{
				Role:    "user",
				Content: "index " + id,
			}},
		}
	}
	return sessions
}
