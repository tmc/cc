package web

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSEBrokerDisconnectsSlowClient(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()

	for i := 0; i < cap(ch); i++ {
		ch <- Event{Type: "queued", Data: i}
	}

	b.Publish(Event{Type: "overflow"})

	if _, ok := <-ch; !ok {
		return
	}
	for range ch {
	}
}

func TestSSEBrokerUnsubscribeIsIdempotent(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()
	b.Unsubscribe(ch)
	b.Unsubscribe(ch)
}

// TestFileWatcherScheduleSingleFlight verifies that schedule never runs two
// reindex passes concurrently and that files arriving while a pass is in
// flight are drained by a trailing pass (not dropped, not run in parallel).
// This is the guard that prevents an actively-written session from stacking
// dozens of overlapping whole-session reparses across cores.
func TestFileWatcherScheduleSingleFlight(t *testing.T) {
	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	var seen sync.Map // file -> times reindexed
	var passes atomic.Int32

	fw := &FileWatcher{
		broker: NewSSEBroker(),
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		reindex: func(ctx context.Context, paths []string) (int, error) {
			n := inFlight.Add(1)
			for {
				m := maxConcurrent.Load()
				if n <= m || maxConcurrent.CompareAndSwap(m, n) {
					break
				}
			}
			passes.Add(1)
			time.Sleep(10 * time.Millisecond) // hold the pass so overlaps would be visible
			for _, p := range paths {
				c, _ := seen.LoadOrStore(p, new(atomic.Int32))
				c.(*atomic.Int32).Add(1)
			}
			inFlight.Add(-1)
			return len(paths), nil
		},
	}

	ctx := context.Background()
	// Fire many batches faster than a pass completes; only one should run at a
	// time and every file must be reindexed at least once.
	for i := 0; i < 20; i++ {
		fw.schedule(ctx, map[string]struct{}{"/p/" + string(rune('a'+i%5)) + ".jsonl": {}})
		time.Sleep(time.Millisecond)
	}

	// Wait for the single-flight chain to drain.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fw.mu.Lock()
		running := fw.running
		fw.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if got := maxConcurrent.Load(); got > 1 {
		t.Fatalf("reindex ran %d passes concurrently; single-flight guard failed", got)
	}
	for _, f := range []string{"/p/a.jsonl", "/p/b.jsonl", "/p/c.jsonl", "/p/d.jsonl", "/p/e.jsonl"} {
		if _, ok := seen.Load(f); !ok {
			t.Errorf("file %s was never reindexed (dropped by single-flight merge)", f)
		}
	}
	if passes.Load() == 0 {
		t.Fatal("no reindex pass ran")
	}
}
