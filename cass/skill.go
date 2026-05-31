package cass

import "time"

// SkillUse is one skill signal observed in a session.
//
// Kind classifies the signal: available, selected, loaded, expanded, or tool.
// Expanded means prompt context was present; it is not counted as skill use.
// Evidence is short text explaining why the skill was recorded.
type SkillUse struct {
	Name      string    `json:"name"`
	Path      string    `json:"path,omitempty"`
	Source    string    `json:"source,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Count     int       `json:"count,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Evidence  []string  `json:"evidence,omitempty"`
}

// SkillHit is a skill joined with its parent session.
type SkillHit struct {
	SkillUse
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent"`
	Title      string `json:"title"`
	Workspace  string `json:"workspace,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	EndedAt    string `json:"ended_at,omitempty"`
}
