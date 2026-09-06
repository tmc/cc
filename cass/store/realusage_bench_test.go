// Benchmarks against a real index, which is the only corpus large enough for
// the usage query budgets to mean anything. They skip unless CASS_REAL_DB
// names an index:
//
//	CASS_REAL_DB=~/.cache/cass/index.db go test -run XXX -bench Real ./cass/store/
package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tmc/cc/cass/store"
)

// realStore opens the index CASS_REAL_DB names, or skips the benchmark.
func realStore(b *testing.B) *store.DB {
	path := os.Getenv("CASS_REAL_DB")
	if path == "" {
		b.Skip("CASS_REAL_DB not set")
	}
	st, err := store.New(path)
	if err != nil {
		b.Fatal(err)
	}
	return st
}

// realFilter is the twelve-week window the budgets are stated for, on an hour
// boundary so that it reads the rollup as a report window does.
func realFilter() store.UsageFilter {
	return store.UsageFilter{After: time.Now().Add(-12 * 7 * 24 * time.Hour).Truncate(time.Hour)}
}

func BenchmarkRealUsageByHour(b *testing.B) {
	st := realStore(b)
	defer st.Close()
	ctx, f := context.Background(), realFilter()
	b.ResetTimer()
	for range b.N {
		if _, err := st.UsageByHour(ctx, f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRealUsageByGroup(b *testing.B) {
	st := realStore(b)
	defer st.Close()
	ctx, f := context.Background(), realFilter()
	b.ResetTimer()
	for range b.N {
		if _, err := st.UsageByGroup(ctx, f); err != nil {
			b.Fatal(err)
		}
	}
}

// eventsFilter is the same window offset by a second, which no rollup row can
// answer, so the query falls back to the per-request table.
func eventsFilter() store.UsageFilter {
	f := realFilter()
	f.After = f.After.Add(time.Second)
	return f
}

func BenchmarkRealUsageByHourEvents(b *testing.B) {
	st := realStore(b)
	defer st.Close()
	ctx, f := context.Background(), eventsFilter()
	b.ResetTimer()
	for range b.N {
		if _, err := st.UsageByHour(ctx, f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRealUsageByGroupEvents(b *testing.B) {
	st := realStore(b)
	defer st.Close()
	ctx, f := context.Background(), eventsFilter()
	b.ResetTimer()
	for range b.N {
		if _, err := st.UsageByGroup(ctx, f); err != nil {
			b.Fatal(err)
		}
	}
}
