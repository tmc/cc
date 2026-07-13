package service

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/store"
)

// newStatsTestService builds a service with n indexed sessions for exercising
// AggregateStats.
func newStatsTestService(t *testing.T, n int) *Service {
	t.Helper()
	svc, err := New(Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{collectorFunc{name: "codex-cli", sessions: serviceTestSessions("z", n)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Index(context.Background(), true); err != nil {
		t.Fatalf("Index: %v", err)
	}
	return svc
}

// TestAggregateStatsConcurrentCoalesce fires many concurrent identical
// AggregateStats requests; the singleflight coalescing must leave every caller
// with the same correct result and no race (run with -race).
func TestAggregateStatsConcurrentCoalesce(t *testing.T) {
	svc := newStatsTestService(t, 200)
	ctx := context.Background()

	want, err := svc.AggregateStats(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("baseline AggregateStats: %v", err)
	}

	const callers = 24
	var wg sync.WaitGroup
	results := make([]map[string]any, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all at once to maximize overlap.
			results[i], errs[i] = svc.AggregateStats(ctx, time.Time{}, time.Time{})
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if !reflect.DeepEqual(results[i], want) {
			t.Fatalf("caller %d result differs from baseline", i)
		}
	}
}

// TestAggregateStatsContextPerCaller verifies a caller observes its own context:
// an already-cancelled context returns promptly with the context error, while a
// concurrent live-context caller still gets a real result. This is the property
// the DoChan + context.WithoutCancel design provides that a naive singleflight
// Do (which binds the shared call to the first caller's context) would break.
func TestAggregateStatsContextPerCaller(t *testing.T) {
	svc := newStatsTestService(t, 50)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.AggregateStats(cancelled, time.Time{}, time.Time{}); err == nil {
		t.Fatal("cancelled caller: want context error, got nil")
	}

	// A subsequent live call must still succeed.
	if _, err := svc.AggregateStats(context.Background(), time.Time{}, time.Time{}); err != nil {
		t.Fatalf("live caller after cancelled one: %v", err)
	}
}

// TestAggregateStatsCachedWithinTTL verifies a repeated identical query within
// aggregateTTL is served from the memo (same map identity, populated cache
// entry) rather than re-running the expensive scan, while a distinct window is a
// separate cache key.
func TestAggregateStatsCachedWithinTTL(t *testing.T) {
	svc := newStatsTestService(t, 50)
	ctx := context.Background()

	first, err := svc.AggregateStats(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("first AggregateStats: %v", err)
	}

	svc.statsMu.Lock()
	n := len(svc.statsCache)
	svc.statsMu.Unlock()
	if n != 1 {
		t.Fatalf("after first call: cache has %d entries, want 1", n)
	}

	second, err := svc.AggregateStats(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("second AggregateStats: %v", err)
	}
	// A cache hit returns the very same map value the first call stored.
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatal("second call returned a different map; expected the cached one")
	}

	// A distinct window is a separate key and must not hit the all-time entry.
	if _, err := svc.AggregateStats(ctx, time.Unix(1, 0), time.Time{}); err != nil {
		t.Fatalf("windowed AggregateStats: %v", err)
	}
	svc.statsMu.Lock()
	n = len(svc.statsCache)
	svc.statsMu.Unlock()
	if n != 2 {
		t.Fatalf("after windowed call: cache has %d entries, want 2", n)
	}
}

// TestAggregateStatsTTLExpiry verifies an entry older than aggregateTTL is
// recomputed: backdating the stored entry forces the next call to re-scan and
// refresh the timestamp.
func TestAggregateStatsTTLExpiry(t *testing.T) {
	svc := newStatsTestService(t, 20)
	ctx := context.Background()

	if _, err := svc.AggregateStats(ctx, time.Time{}, time.Time{}); err != nil {
		t.Fatalf("first AggregateStats: %v", err)
	}
	key := time.Time{}.UTC().Format(time.RFC3339Nano) + "|" + time.Time{}.UTC().Format(time.RFC3339Nano)

	svc.statsMu.Lock()
	stale := svc.statsCache[key]
	stale.at = stale.at.Add(-2 * aggregateTTL) // age past the TTL.
	svc.statsCache[key] = stale
	staleAt := stale.at
	svc.statsMu.Unlock()

	if _, err := svc.AggregateStats(ctx, time.Time{}, time.Time{}); err != nil {
		t.Fatalf("second AggregateStats: %v", err)
	}

	svc.statsMu.Lock()
	freshAt := svc.statsCache[key].at
	svc.statsMu.Unlock()
	if !freshAt.After(staleAt) {
		t.Fatalf("entry not refreshed after TTL expiry: at=%v stale=%v", freshAt, staleAt)
	}
}

// TestStatsIncludesAPIRequestCount verifies the basic stats contract carries
// the request count that the UI surface now expects.
func TestStatsIncludesAPIRequestCount(t *testing.T) {
	db, err := store.New(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.BatchIndexRequests(context.Background(), []cass.APIRequest{{
		ID:        "req-1",
		SessionID: "session-1",
		RequestID: "request-1",
		Timestamp: 100,
		Method:    "POST",
	}}); err != nil {
		t.Fatalf("BatchIndexRequests: %v", err)
	}

	svc := &Service{store: db}
	stats, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if got := stats["api_request_count"]; got != 1 {
		t.Fatalf("api_request_count = %#v, want 1", got)
	}
}
