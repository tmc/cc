package cass

import (
	"strings"
	"time"
)

// SkillUse is one skill signal observed in a session.
//
// Kind classifies the signal: available, selected, loaded, or expanded.
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

// SkillUseKinds are the signal kinds that count as using a skill, as opposed
// to having it available or in context.
var SkillUseKinds = []string{"selected", "loaded"}

// SkillQuery selects skill signals for lookup.
//
// Skill matches a skill name without regard to case or plugin prefix:
// "deep-research" matches "anthropic-skills:deep-research". It may be a glob
// pattern such as "go-team-*". A value containing "/" matches a substring of
// the skill's path instead. The empty string matches every skill.
//
// Kinds lists the signal kinds to include. Nil means [SkillUseKinds].
type SkillQuery struct {
	Skill     string
	Kinds     []string
	Agent     string    // harness, such as claude-code or codex (matching codex-cli)
	Workspace string    // workspace path or project name substring
	Since     time.Time // signals last seen at or after this time
	Limit     int
}

// SkillSession is one session's skill signals that matched a [SkillQuery].
type SkillSession struct {
	SessionID  string     `json:"session_id"`
	Agent      string     `json:"agent"`
	Title      string     `json:"title"`
	Workspace  string     `json:"workspace,omitempty"`
	SourcePath string     `json:"source_path,omitempty"`
	StartedAt  string     `json:"started_at,omitempty"`
	EndedAt    string     `json:"ended_at,omitempty"`
	LastSeen   time.Time  `json:"last_seen,omitzero"`
	Skills     []SkillUse `json:"skills"`
}

// SkillStat summarizes one skill's use across sessions.
// Name is the skill name without its plugin prefix.
type SkillStat struct {
	Name      string         `json:"name"`
	Sessions  int            `json:"sessions"`
	Agents    map[string]int `json:"agents"` // sessions per harness
	FirstSeen time.Time      `json:"first_seen,omitzero"`
	LastSeen  time.Time      `json:"last_seen,omitzero"`
}

// ParseSkillKinds parses a comma-separated kind list for [SkillQuery.Kinds].
// The empty string means the kinds that count as use, and "all" means every
// kind, including skills that were only available or in context.
func ParseSkillKinds(s string) []string {
	switch strings.TrimSpace(s) {
	case "":
		return nil
	case "all":
		return []string{}
	}
	var kinds []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}
	return kinds
}
