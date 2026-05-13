package collector

import (
	"testing"
	"time"

	"github.com/tmc/cc"
)

func TestExtractClaudeGoals(t *testing.T) {
	t0 := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	t2 := t1.Add(time.Minute)
	entries := []cc.Entry{
		{
			Type:      "attachment",
			Timestamp: t0,
			Attachment: &cc.Attachment{
				Type:      "goal_status",
				Condition: "ship the thing",
				Sentinel:  true,
			},
		},
		{
			Type:      "attachment",
			Timestamp: t1,
			Attachment: &cc.Attachment{
				Type:      "goal_status",
				Condition: "ship the thing",
				Reason:    "still building",
			},
		},
		{
			Type:      "attachment",
			Timestamp: t2,
			Attachment: &cc.Attachment{
				Type:      "goal_status",
				Condition: "ship the thing",
				Met:       true,
			},
		},
		// Non-goal attachment must be ignored.
		{
			Type:       "attachment",
			Timestamp:  t2,
			Attachment: &cc.Attachment{Type: "skill_listing"},
		},
	}

	goals := ExtractClaudeGoals(entries)
	if len(goals) != 1 {
		t.Fatalf("got %d goals want 1: %+v", len(goals), goals)
	}
	g := goals[0]
	if g.Objective != "ship the thing" {
		t.Errorf("objective = %q", g.Objective)
	}
	if g.Status != "completed" || g.EffectiveStatus != "completed" {
		t.Errorf("status = %q effective = %q", g.Status, g.EffectiveStatus)
	}
	if !g.CreatedAt.Equal(t0) || !g.LastObservedAt.Equal(t2) {
		t.Errorf("timestamps wrong: created=%v last=%v", g.CreatedAt, g.LastObservedAt)
	}
}
