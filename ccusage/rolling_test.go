package ccusage

import (
	"testing"
	"time"
)

func TestRolling(t *testing.T) {
	end := time.Unix(10000, 0)
	records := []Record{
		{SessionID: "a", Time: end.Add(-time.Hour), Input: 10, Output: 2},
		{SessionID: "a", Time: end.Add(-time.Minute), Input: 20, CacheRead: 3},
		{SessionID: "b", Time: end.Add(-time.Minute), Output: 4},
		{SessionID: "outside", Time: end.Add(-time.Hour - time.Second), Input: 100},
		{SessionID: "end", Time: end, Input: 100},
		{SessionID: "future", Time: end.Add(time.Second), Input: 100},
		{SessionID: "undated", Input: 100},
	}
	for _, tt := range []struct {
		name         string
		duration     time.Duration
		wantA, wantB int
	}{
		{"hour", time.Hour, 35, 4},
		{"minute", time.Minute, 23, 4},
		{"zero", 0, 0, 0},
		{"negative", -time.Second, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spans := Rolling(records, end, tt.duration)
			got := map[string]int{}
			for _, span := range spans {
				got[span.SessionID] = span.Total()
			}
			if got["a"] != tt.wantA || got["b"] != tt.wantB {
				t.Fatalf("totals = %v", got)
			}
			if len(got) > 2 {
				t.Fatalf("outside records included: %v", got)
			}
		})
	}
	_, _, spans := Aggregate(records[:2])
	if len(spans) != 1 || spans[0].Requests != 2 || spans[0].Total() != 35 {
		t.Fatalf("aggregate = %+v", spans)
	}
}
