package collector

import (
	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// ExtractClaudeGoals collapses goal_status attachment entries from a Claude
// Code session into the cass.Goal model. The CLI emits one attachment per
// status check; the first carries sentinel=true and the rest update met/reason
// for the same condition. Goals are keyed by condition text.
func ExtractClaudeGoals(entries []cc.Entry) []cass.Goal {
	byCondition := map[string]*cass.Goal{}
	var order []string
	for _, e := range entries {
		if e.Attachment == nil || e.Attachment.Type != "goal_status" {
			continue
		}
		cond := e.Attachment.Condition
		if cond == "" {
			continue
		}
		g, ok := byCondition[cond]
		if !ok {
			g = &cass.Goal{
				Objective: cond,
				CreatedAt: e.Timestamp,
			}
			byCondition[cond] = g
			order = append(order, cond)
		}
		g.LastObservedAt = e.Timestamp
		g.UpdatedAt = e.Timestamp
		if e.Attachment.Met {
			g.Status = "completed"
		} else if g.Status == "" {
			g.Status = "active"
		}
		if e.Attachment.Reason != "" {
			g.CompletionBudgetReport = e.Attachment.Reason
		}
	}
	out := make([]cass.Goal, 0, len(order))
	for _, cond := range order {
		out = append(out, cass.NormalizeGoal(*byCondition[cond]))
	}
	return out
}
