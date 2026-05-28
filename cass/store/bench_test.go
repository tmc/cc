package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/store"
)

// makeSessions generates n synthetic sessions each with ~targetBytes of content.
// The content is intentionally repetitive to stress FTS posting-list inflation.
func makeSessions(n, targetBytes int) []cass.Session {
	para := strings.Repeat("The quick brown fox jumps over the lazy dog. ", targetBytes/46+1)
	if len(para) > targetBytes {
		para = para[:targetBytes]
	}

	sessions := make([]cass.Session, n)
	for i := range sessions {
		sessions[i] = cass.Session{
			ID:        fmt.Sprintf("sess-%05d", i),
			Agent:     "claude-code",
			Title:     fmt.Sprintf("Session %d: refactor authentication middleware", i),
			Workspace: "/home/user/projects/app",
			StartedAt: time.Now().Add(-time.Duration(n-i) * time.Minute),
			EndedAt:   time.Now().Add(-time.Duration(n-i-1) * time.Minute),
			Messages: []cass.Message{
				{Role: "user", Content: para[:targetBytes/2]},
				{Role: "assistant", Content: para[targetBytes/2:]},
			},
			Stats: cass.SessionStats{
				ToolCalls:    42,
				InputTokens:  8000,
				OutputTokensSnapshot: 2000,
				Turns:        5,
				DurationSecs: 300,
			},
		}
	}
	return sessions
}

// BenchmarkSQLiteIndex measures index time and size for the default SQLite backend.
func BenchmarkSQLiteIndex(b *testing.B) {
	benchmarkBackend(b, store.BackendSQLite)
}

// BenchmarkSQLitePorterIndex measures index time and size with porter stemming.
func BenchmarkSQLitePorterIndex(b *testing.B) {
	benchmarkBackend(b, store.BackendSQLitePorter)
}

func benchmarkBackend(b *testing.B, kind store.BackendKind) {
	b.Helper()
	const (
		nSessions   = 200
		bytesPerSess = 10_000
	)
	sessions := makeSessions(nSessions, bytesPerSess)

	b.ResetTimer()
	for range b.N {
		st, err := store.NewWithConfig(store.BackendConfig{Kind: kind, Path: ":memory:"})
		if err != nil {
			b.Fatal(err)
		}
		if err := st.BatchIndex(context.Background(), sessions); err != nil {
			b.Fatal(err)
		}
		st.Close()
	}
	b.SetBytes(int64(nSessions * bytesPerSess))
}

// TestBackendSizes is a manual/exploratory test (not run by default) that
// reports index overhead for each SQLite variant.
// Run with: go test -run TestBackendSizes -v ./cass/store/
func TestBackendSizes(t *testing.T) {
	const (
		nSessions    = 500
		bytesPerSess = 5_000
	)
	sessions := makeSessions(nSessions, bytesPerSess)
	rawBytes := int64(nSessions * bytesPerSess)

	for _, kind := range []store.BackendKind{store.BackendSQLite, store.BackendSQLitePorter} {
		st, err := store.NewWithConfig(store.BackendConfig{Kind: kind, Path: ":memory:"})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.BatchIndex(context.Background(), sessions); err != nil {
			t.Fatal(err)
		}

		stats, err := st.BackendStats(context.Background())
		if err != nil {
			t.Logf("%s: BackendStats: %v", kind, err)
		} else {
			t.Logf("%s: rows=%d  fts_index=%d KB  ratio=%.1fx  raw=%d KB",
				kind, stats.TotalRows,
				stats.IndexSizeBytes/1024,
				float64(stats.IndexSizeBytes)/float64(rawBytes),
				rawBytes/1024,
			)
		}
		st.Close()
	}
}
