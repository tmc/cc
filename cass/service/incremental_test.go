package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func claudeLine(uuid, text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":      "user",
		"timestamp": "2026-05-01T10:00:00Z",
		"uuid":      uuid,
		"sessionId": "sess-inc-1",
		"cwd":       "/work/inc",
		"message":   map[string]any{"role": "user", "content": text},
	})
	return append(b, '\n')
}

// claudeProject writes a session JSONL under an encoded project dir and returns
// its path. The dir layout mirrors ~/.claude/projects/<encoded>/<uuid>.jsonl.
func claudeProject(t *testing.T, root string) string {
	t.Helper()
	projDir := filepath.Join(root, "projects", "-work-inc")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(projDir, "sess-inc-1.jsonl")
}

// TestIndexPathsIncrementalMatchesFull verifies that indexing a file in two
// IndexPaths passes (the second served by the tail cache) produces the same
// stored session as a single full IndexRoots of the final bytes.
func TestIndexPathsIncrementalMatchesFull(t *testing.T) {
	ctx := context.Background()

	// Incremental service: write 2 entries, IndexPaths, append 2 more, IndexPaths.
	rootA := t.TempDir()
	pathA := claudeProject(t, rootA)
	os.WriteFile(pathA, append(claudeLine("a", "first message about parsers"),
		claudeLine("b", "second message about indexes")...), 0o644)
	setFileMtime(t, pathA, time.Unix(1_000_000, 0))

	svcA, err := New(Config{DBPath: filepath.Join(rootA, "index.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svcA.Close() })

	if _, err := svcA.IndexPaths(ctx, []string{pathA}); err != nil {
		t.Fatalf("IndexPaths 1: %v", err)
	}
	appendFile(t, pathA, append(claudeLine("c", "third message about caching"),
		claudeLine("d", "fourth message about offsets")...))
	setFileMtime(t, pathA, time.Unix(1_000_060, 0)) // mtime advances
	if _, err := svcA.IndexPaths(ctx, []string{pathA}); err != nil {
		t.Fatalf("IndexPaths 2: %v", err)
	}

	hitA := lookupOnlySession(t, ctx, svcA)

	// Full service: index the identical final file in one shot.
	rootB := t.TempDir()
	pathB := claudeProject(t, rootB)
	finalBytes, _ := os.ReadFile(pathA)
	os.WriteFile(pathB, finalBytes, 0o644)

	svcB, err := New(Config{DBPath: filepath.Join(rootB, "index.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svcB.Close() })
	if _, err := svcB.IndexRoots(ctx, []string{pathB}); err != nil {
		t.Fatalf("IndexRoots B: %v", err)
	}
	hitB := lookupOnlySession(t, ctx, svcB)

	// The incremental-built row must match the full-built row on the fields
	// derived from the full entry set.
	if hitA.ToolCalls != hitB.ToolCalls {
		t.Errorf("ToolCalls: incremental=%d full=%d", hitA.ToolCalls, hitB.ToolCalls)
	}
	if hitA.EndedAt != hitB.EndedAt {
		t.Errorf("EndedAt: incremental=%q full=%q", hitA.EndedAt, hitB.EndedAt)
	}
	if hitA.Title != hitB.Title {
		t.Errorf("Title: incremental=%q full=%q", hitA.Title, hitB.Title)
	}

	// Content from the appended (tailed) entries must be searchable in the
	// incrementally-indexed DB.
	res, err := svcA.Search(ctx, cass.SearchRequest{Query: "offsets", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Errorf("tailed content 'offsets' not searchable after incremental index")
	}
}

// lookupOnlySession returns the single indexed session via a broad search
// (the session ID is derived from the file path, so it is resolved by query).
func lookupOnlySession(t *testing.T, ctx context.Context, svc *Service) cass.Hit {
	t.Helper()
	res, err := svc.Search(ctx, cass.SearchRequest{Query: "message", Limit: 5})
	if err != nil {
		t.Fatalf("search for session: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no indexed session found")
	}
	hit, err := svc.Session(ctx, res.Hits[0].SessionID)
	if err != nil {
		t.Fatalf("Session(%s): %v", res.Hits[0].SessionID, err)
	}
	return hit
}

func setFileMtime(t *testing.T, path string, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	f.Close()
}
