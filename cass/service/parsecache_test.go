package service

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func pcRow(uuid string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":      "user",
		"timestamp": "2026-04-20T10:00:00Z",
		"uuid":      uuid,
		"message":   map[string]any{"role": "user", "content": "msg " + uuid},
	})
	return append(b, '\n')
}

// monoClock yields strictly increasing timestamps, mirroring how a real
// filesystem advances a file's mtime across sequential writes. Tests use it via
// setMtime so a same-size rewrite is still seen as newer, without the
// non-monotonic collisions that a relative "add to current mtime" would produce
// (real session logs are append-only and never reuse an earlier mtime).
type monoClock struct{ t time.Time }

func newMonoClock() *monoClock {
	return &monoClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (m *monoClock) next() time.Time {
	m.t = m.t.Add(time.Second)
	return m.t
}

func setMtime(t *testing.T, path string, mt time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// TestParseCache_EquivalenceUnderRandomOps is the core correctness guard: after
// every file mutation, ParseFile must return exactly what a fresh cc.ReadFile
// returns. A divergence here is a silent index-corruption bug.
func TestParseCache_EquivalenceUnderRandomOps(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))

	for trial := 0; trial < 40; trial++ {
		tmp := t.TempDir()
		path := filepath.Join(tmp, "s.jsonl")
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		cache := NewParseCache(1 << 20)
		clk := newMonoClock()
		next := 0

		for step := 0; step < 30; step++ {
			switch rng.Intn(7) {
			case 0, 1, 2: // append one or more complete lines
				for k := 0; k < 1+rng.Intn(3); k++ {
					appendBytes(t, path, pcRow(uuidf(trial, next)))
					next++
				}
			case 3: // append a partial (unterminated, invalid) half-line
				row := pcRow(uuidf(trial, next))
				appendBytes(t, path, row[:len(row)/2]) // no newline
			case 4: // truncate to zero
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
				next = 0
			case 5: // rewrite from scratch with a fresh set (grows or shrinks)
				var buf []byte
				m := rng.Intn(4)
				for k := 0; k < m; k++ {
					buf = append(buf, pcRow(uuidf(trial, 1000+step*10+k))...)
				}
				if err := os.WriteFile(path, buf, 0o644); err != nil {
					t.Fatal(err)
				}
			case 6: // append a COMPLETE but unterminated final line (mid-write
				// flush): cc.ReadFile decodes it, so ParseFile must too via the
				// returned partial, while keeping the cached offset on a boundary.
				row := pcRow(uuidf(trial, next))
				appendBytes(t, path, row[:len(row)-1]) // drop only the '\n'
				next++
			}
			// Every mutation advances mtime monotonically, as a real filesystem
			// does for sequential writes.
			setMtime(t, path, clk.next())

			got, err := cache.ParseFile(ctx, path)
			if err != nil {
				t.Fatalf("trial %d step %d: ParseFile: %v", trial, step, err)
			}
			want, err := cc.ReadFile(ctx, path)
			if err != nil {
				t.Fatalf("trial %d step %d: ReadFile: %v", trial, step, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial %d step %d: ParseFile != ReadFile\n got %d entries\n want %d entries", trial, step, len(got), len(want))
			}
		}
	}
}

func uuidf(a, b int) string {
	return "u-" + itoa(a) + "-" + itoa(b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestParseCache_SkipReturnsCachedWithoutReparse verifies the unchanged-file
// fast path returns the same entries without re-reading.
func TestParseCache_SkipReturnsCachedWithoutReparse(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	appendBytes(t, path, pcRow("a"))
	appendBytes(t, path, pcRow("b"))

	cache := NewParseCache(1 << 20)
	first, err := cache.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// No change: second call is a skip and must equal the first and a full read.
	second, err := cache.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := cc.ReadFile(ctx, path)
	if !reflect.DeepEqual(first, full) || !reflect.DeepEqual(second, full) {
		t.Fatalf("skip path diverged from full read")
	}
}

// TestParseCache_TailMatchesFullAfterGrowth checks the grow path.
func TestParseCache_TailMatchesFullAfterGrowth(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	clk := newMonoClock()
	appendBytes(t, path, pcRow("a"))
	setMtime(t, path, clk.next())
	cache := NewParseCache(1 << 20)
	if _, err := cache.ParseFile(ctx, path); err != nil {
		t.Fatal(err)
	}

	appendBytes(t, path, pcRow("b"))
	appendBytes(t, path, pcRow("c"))
	setMtime(t, path, clk.next())

	got, err := cache.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := cc.ReadFile(ctx, path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tail != full: got %d want %d", len(got), len(want))
	}
}

// TestParseCache_EvictionFallsBackToFull verifies an evicted entry is re-read
// in full and that a slice returned before eviction is not corrupted.
func TestParseCache_EvictionFallsBackToFull(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.jsonl")
	b := filepath.Join(tmp, "b.jsonl")
	appendBytes(t, a, pcRow("a1"))
	appendBytes(t, b, pcRow("b1"))

	cache := NewParseCache(600) // tiny: holds ~one entry, forces eviction.
	gotA, err := cache.ParseFile(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	heldA := append([]cc.Entry(nil), gotA...)
	// Parse b; this should evict a under the tiny budget.
	if _, err := cache.ParseFile(ctx, b); err != nil {
		t.Fatal(err)
	}
	// a is now (likely) evicted; ParseFile(a) falls back to full and matches.
	reA, err := cache.ParseFile(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	fullA, _ := cc.ReadFile(ctx, a)
	if !reflect.DeepEqual(reA, fullA) {
		t.Fatalf("post-eviction read diverged")
	}
	// The earlier slice is still intact (copy-on-return).
	if !reflect.DeepEqual(heldA, fullA) {
		t.Fatalf("held slice corrupted by eviction")
	}
}

// TestParseCache_NilIsFullReadNoop verifies a nil cache reads in full.
func TestParseCache_NilIsFullReadNoop(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	appendBytes(t, path, pcRow("a"))
	var cache *ParseCache
	got, err := cache.ParseFile(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := cc.ReadFile(ctx, path)
	if !reflect.DeepEqual(got, full) {
		t.Fatalf("nil cache diverged from full read")
	}
}
