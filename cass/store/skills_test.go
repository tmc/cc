package store

import (
	"context"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func skillSession(id, agent, workspace string, at time.Time, skills ...cass.SkillUse) cass.Session {
	for i := range skills {
		skills[i].Count = 1
		skills[i].FirstSeen = at
		skills[i].LastSeen = at
	}
	return cass.Session{
		ID:        id,
		Agent:     agent,
		Title:     id,
		Workspace: workspace,
		StartedAt: at,
		EndedAt:   at,
		Skills:    skills,
	}
}

func TestSkillLookup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	if err := s.BatchIndex(ctx, []cass.Session{
		skillSession("claude-1", "claude-code", "/w/cc", now.Add(-3*time.Hour),
			cass.SkillUse{Name: "anthropic-skills:deep-research", Kind: "selected"},
			cass.SkillUse{Name: "deep-research", Kind: "loaded", Path: "/s/deep-research/SKILL.md"}),
		skillSession("codex-1", "codex-cli", "/w/nac", now.Add(-1*time.Hour),
			cass.SkillUse{Name: "Deep-Research", Kind: "selected"},
			cass.SkillUse{Name: "go-team-history-audit", Kind: "selected"},
			cass.SkillUse{Name: "imagegen", Kind: "available"}),
		skillSession("pi-1", "pi", "/w/cc", now.Add(-48*time.Hour),
			cass.SkillUse{Name: "go-team-codebase-review", Kind: "loaded", Path: "/p/go-team-codebase-review/SKILL.md"}),
	}); err != nil {
		t.Fatal(err)
	}

	ids := func(q cass.SkillQuery) []string {
		t.Helper()
		got, err := s.SkillSessions(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, ss := range got {
			ids = append(ids, ss.SessionID)
		}
		return ids
	}
	tests := []struct {
		name string
		q    cass.SkillQuery
		want []string
	}{
		{"bare name matches prefixed and any case", cass.SkillQuery{Skill: "deep-research"}, []string{"codex-1", "claude-1"}},
		{"prefixed name matches bare", cass.SkillQuery{Skill: "anthropic-skills:deep-research"}, []string{"codex-1", "claude-1"}},
		{"glob", cass.SkillQuery{Skill: "go-team-*"}, []string{"codex-1", "pi-1"}},
		{"prefixed glob keeps prefix", cass.SkillQuery{Skill: "anthropic-skills:*"}, []string{"claude-1"}},
		{"path substring", cass.SkillQuery{Skill: "/p/go-team"}, []string{"pi-1"}},
		{"available is not use", cass.SkillQuery{Skill: "imagegen"}, nil},
		{"all kinds", cass.SkillQuery{Skill: "imagegen", Kinds: []string{}}, []string{"codex-1"}},
		{"agent prefix", cass.SkillQuery{Skill: "deep-research", Agent: "codex"}, []string{"codex-1"}},
		{"workspace", cass.SkillQuery{Workspace: "/w/cc"}, []string{"claude-1", "pi-1"}},
		{"since", cass.SkillQuery{Skill: "go-team-*", Since: now.Add(-2 * time.Hour)}, []string{"codex-1"}},
		{"limit counts sessions", cass.SkillQuery{Limit: 1}, []string{"codex-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ids(tt.q)
			if len(got) != len(tt.want) {
				t.Fatalf("sessions = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("sessions = %q, want %q", got, tt.want)
				}
			}
		})
	}

	stats, err := s.SkillStats(ctx, cass.SkillQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 || stats[0].Name != "deep-research" || stats[0].Sessions != 2 ||
		stats[0].Agents["claude-code"] != 1 || stats[0].Agents["codex-cli"] != 1 {
		t.Fatalf("SkillStats = %+v, want deep-research first with one claude-code and one codex-cli session", stats)
	}

	res, err := s.Search(ctx, cass.SearchRequest{Limit: 10, Filters: cass.Filters{Skill: "go-team-*"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalCount != 2 {
		t.Fatalf("search -skill go-team-* = %d hits, want 2", res.TotalCount)
	}
}

func TestReplaceSessionSkills(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	if err := s.BatchIndex(ctx, []cass.Session{skillSession("a", "codex-cli", "/w", now)}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReplaceSessionSkills(ctx, []cass.Session{
		skillSession("a", "codex-cli", "/w", now, cass.SkillUse{Name: "nlm", Kind: "selected"}),
		skillSession("unindexed", "codex-cli", "/w", now, cass.SkillUse{Name: "nlm", Kind: "selected"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ReplaceSessionSkills = %d, want 1 (unindexed sessions are skipped)", n)
	}
	got, err := s.SkillSessions(ctx, cass.SkillQuery{Skill: "nlm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "a" || got[0].Agent != "codex-cli" {
		t.Fatalf("SkillSessions = %+v, want session a", got)
	}

	if err := s.Delete(ctx, cass.DeleteFilter{IDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM session_skills").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("after delete, session_skills has %d rows, want 0", rows)
	}
}
