package service

import (
	"context"
	"fmt"
	"os"
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

func TestIndexRootsCodexFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "index.db")
	sessionPath := filepath.Join(root, ".codex", "sessions", "2026", "05", "03", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"timestamp":"2026-05-03T12:00:00Z","type":"session_meta","payload":{"id":"codex-root-1","cwd":"/work/root","originator":"codex-tui","source":"cli"}}
{"timestamp":"2026-05-03T12:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"targeted path import"}]}}
`
	if err := os.WriteFile(sessionPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	svc, err := New(Config{DBPath: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })

	n, err := svc.IndexRoots(ctx, []string{sessionPath})
	if err != nil {
		t.Fatalf("IndexRoots: %v", err)
	}
	if n != 1 {
		t.Fatalf("IndexRoots = %d, want 1", n)
	}
	hit, err := svc.Session(ctx, "codex-root-1")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if hit.Workspace != "/work/root" || hit.SourcePath != sessionPath {
		t.Fatalf("hit = %#v", hit)
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
