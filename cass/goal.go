package cass

import "time"

// Goal is one goal-mode objective observed in a session.
type Goal struct {
	ThreadID               string     `json:"thread_id,omitempty"`
	Objective              string     `json:"objective"`
	Status                 string     `json:"status,omitempty"`
	EffectiveStatus        string     `json:"effective_status,omitempty"`
	CompletionGates        []GoalGate `json:"completion_gates,omitempty"`
	TokenBudget            *int       `json:"token_budget,omitempty"`
	TokensUsed             int        `json:"tokens_used,omitempty"`
	TimeUsedSeconds        int        `json:"time_used_seconds,omitempty"`
	CreatedAt              time.Time  `json:"created_at,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at,omitempty"`
	LastObservedAt         time.Time  `json:"last_observed_at,omitempty"`
	CompletionBudgetReport string     `json:"completion_budget_report,omitempty"`
}

// GoalGate is one observed completion requirement for a goal.
type GoalGate struct {
	Name       string    `json:"name"`
	Status     string    `json:"status,omitempty"` // required, missing, blocked, complete
	Source     string    `json:"source,omitempty"`
	Evidence   string    `json:"evidence,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

// GoalEffectiveStatus reports the status users should trust.
func GoalEffectiveStatus(g Goal) string {
	if GoalUnresolvedGateCount(g) > 0 {
		return "blocked"
	}
	if g.Status != "" {
		return g.Status
	}
	return "active"
}

// GoalUnresolvedGateCount reports required, missing, or blocked gates.
func GoalUnresolvedGateCount(g Goal) int {
	n := 0
	for _, gate := range g.CompletionGates {
		switch gate.Status {
		case "", "required", "missing", "blocked":
			n++
		}
	}
	return n
}

// NormalizeGoal records the derived status in the JSON form.
func NormalizeGoal(g Goal) Goal {
	g.EffectiveStatus = GoalEffectiveStatus(g)
	return g
}

// GoalHit is a goal joined with its parent session.
type GoalHit struct {
	Goal
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	Title      string `json:"title"`
	Workspace  string `json:"workspace,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}
