package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func newTestStore(t *testing.T) *DB {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestBatchIndexAndSearch(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sessions := []cass.Session{
		{
			ID:        "s1",
			Agent:     "claude-code",
			Title:     "Fix authentication bug",
			Workspace: "/home/user/project",
			StartedAt: time.Now().Add(-2 * time.Hour),
			EndedAt:   time.Now().Add(-time.Hour),
			Messages: []cass.Message{
				{Role: "user", Content: "there's a bug in the auth handler"},
				{Role: "assistant", Content: "I see the issue in auth.go line 42"},
			},
			Stats: cass.SessionStats{
				SubagentEntries:         4,
				SubagentMirroredEntries: 3,
				AgentProgressEvents:     5,
				AgentProgressMirrors:    3,
				AgentProgressUnmatched:  2,
			},
		},
		{
			ID:        "s2",
			Agent:     "cursor",
			Title:     "Add dark mode",
			Workspace: "/home/user/frontend",
			StartedAt: time.Now().Add(-3 * time.Hour),
			EndedAt:   time.Now().Add(-2 * time.Hour),
			Messages: []cass.Message{
				{Role: "user", Content: "implement dark mode theme"},
				{Role: "assistant", Content: "I'll update the CSS variables"},
			},
		},
		{
			ID:        "s3",
			Agent:     "claude-code",
			Title:     "Database migration script",
			Workspace: "/home/user/project",
			StartedAt: time.Now().Add(-time.Hour),
			EndedAt:   time.Now(),
			Messages: []cass.Message{
				{Role: "user", Content: "write a migration to add users table"},
				{Role: "assistant", Content: "here's the SQL migration for github.com/tmc/gocp"},
			},
		},
	}

	if err := s.BatchIndex(ctx, sessions); err != nil {
		t.Fatal(err)
	}

	count, err := s.SessionCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("got count %d, want 3", count)
	}

	tests := []struct {
		name      string
		req       cass.SearchRequest
		wantCount int
		wantFirst string
	}{
		{
			name:      "search by keyword",
			req:       cass.SearchRequest{Query: "auth", Limit: 10},
			wantCount: 1,
			wantFirst: "s1",
		},
		{
			name:      "search by agent filter",
			req:       cass.SearchRequest{Filters: cass.Filters{Agent: "cursor"}, Limit: 10},
			wantCount: 1,
			wantFirst: "s2",
		},
		{
			name:      "search all",
			req:       cass.SearchRequest{Limit: 10},
			wantCount: 3,
		},
		{
			name:      "search with workspace filter",
			req:       cass.SearchRequest{Filters: cass.Filters{Workspace: "frontend"}, Limit: 10},
			wantCount: 1,
			wantFirst: "s2",
		},
		{
			name:      "search dark mode",
			req:       cass.SearchRequest{Query: "dark mode", Limit: 10},
			wantCount: 1,
			wantFirst: "s2",
		},
		{
			name:      "search punctuation",
			req:       cass.SearchRequest{Query: "github.com/tmc/gocp", Limit: 10},
			wantCount: 1,
			wantFirst: "s3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := s.Search(ctx, tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if result.TotalCount != tt.wantCount {
				t.Errorf("got %d results, want %d", result.TotalCount, tt.wantCount)
			}
			if tt.wantFirst != "" && len(result.Hits) > 0 && result.Hits[0].SessionID != tt.wantFirst {
				t.Errorf("first hit %q, want %q", result.Hits[0].SessionID, tt.wantFirst)
			}
		})
	}

	hit, err := s.Session(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if hit.SubagentEntries != 4 || hit.SubagentMirroredEntries != 3 {
		t.Errorf("subagent mirror fields = (%d, %d), want (4, 3)", hit.SubagentEntries, hit.SubagentMirroredEntries)
	}
	if hit.AgentProgressEvents != 5 || hit.AgentProgressMirrors != 3 || hit.AgentProgressUnmatched != 2 {
		t.Errorf("agent progress fields = (%d, %d, %d), want (5, 3, 2)",
			hit.AgentProgressEvents, hit.AgentProgressMirrors, hit.AgentProgressUnmatched)
	}
}

func TestStandaloneBackendSearchPunctuation(t *testing.T) {
	s, err := NewWithConfig(BackendConfig{Kind: BackendSQLite, Path: ":memory:"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ctx := context.Background()
	if err := s.BatchIndex(ctx, []cass.Session{{
		ID:        "path-1",
		Agent:     "codex-cli",
		Title:     "Path search",
		Workspace: "/home/user/gocp",
		StartedAt: time.Unix(100, 0),
		EndedAt:   time.Unix(200, 0),
		Messages: []cass.Message{
			{Role: "user", Content: "look at github.com/tmc/gocp before editing"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	result, err := s.Search(ctx, cass.SearchRequest{Query: "github.com/tmc/gocp", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 || len(result.Hits) != 1 || result.Hits[0].SessionID != "path-1" {
		t.Fatalf("Search = %+v, want path-1", result)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sessions := []cass.Session{
		{ID: "d1", Agent: "claude-code", Title: "session 1", Messages: []cass.Message{{Role: "user", Content: "hello"}}},
		{ID: "d2", Agent: "cursor", Title: "session 2", Messages: []cass.Message{{Role: "user", Content: "world"}}},
	}
	if err := s.BatchIndex(ctx, sessions); err != nil {
		t.Fatal(err)
	}

	// Delete by ID.
	if err := s.Delete(ctx, cass.DeleteFilter{IDs: []string{"d1"}}); err != nil {
		t.Fatal(err)
	}
	count, _ := s.SessionCount(ctx)
	if count != 1 {
		t.Errorf("got count %d after delete, want 1", count)
	}

	// Delete by agent.
	if err := s.Delete(ctx, cass.DeleteFilter{Agent: "cursor"}); err != nil {
		t.Fatal(err)
	}
	count, _ = s.SessionCount(ctx)
	if count != 0 {
		t.Errorf("got count %d after agent delete, want 0", count)
	}
}

func TestMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetMeta(ctx, "schema_version", "1"); err != nil {
		t.Fatal(err)
	}
	v, err := s.Meta(ctx, "schema_version")
	if err != nil {
		t.Fatal(err)
	}
	if v != "1" {
		t.Errorf("got %q, want %q", v, "1")
	}

	// Missing key returns empty string.
	v, err = s.Meta(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if v != "" {
		t.Errorf("got %q for missing key, want empty", v)
	}
}

func TestTeamFieldsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sessions := []cass.Session{
		{
			ID:         "team-lead-1",
			Agent:      "claude-code",
			Title:      "Team lead session",
			TeamName:   "work-team",
			IsTeamLead: true,
			Messages:   []cass.Message{{Role: "user", Content: "start team"}},
			Metadata: map[string]any{
				"session_links": []cass.SessionLink{
					{
						SourceSession: "team-lead",
						TargetSession: "researcher",
						Kind:          "team",
						Action:        "team-spawn",
						TeamName:      "work-team",
					},
				},
			},
		},
		{
			ID:         "team-member-1",
			Agent:      "claude-code",
			Title:      "Member session",
			TeamName:   "work-team",
			AgentName:  "researcher",
			IsTeamLead: false,
			Messages:   []cass.Message{{Role: "user", Content: "do research"}},
		},
	}

	if err := s.BatchIndex(ctx, sessions); err != nil {
		t.Fatal(err)
	}

	// Search for the lead session.
	result, err := s.Search(ctx, cass.SearchRequest{Query: "start team", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(result.Hits))
	}
	lead := result.Hits[0]
	if !lead.IsTeamLead {
		t.Error("IsTeamLead = false, want true")
	}
	if lead.TeamName != "work-team" {
		t.Errorf("TeamName = %q, want %q", lead.TeamName, "work-team")
	}

	// Search for the member session.
	result, err = s.Search(ctx, cass.SearchRequest{Query: "do research", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("got %d hits, want 1", len(result.Hits))
	}
	member := result.Hits[0]
	if member.IsTeamLead {
		t.Error("IsTeamLead = true for member, want false")
	}
	if member.AgentName != "researcher" {
		t.Errorf("AgentName = %q, want %q", member.AgentName, "researcher")
	}

	// Verify team filter works.
	result, err = s.Search(ctx, cass.SearchRequest{
		Filters: cass.Filters{Team: "work-team"},
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 2 {
		t.Errorf("team filter got %d results, want 2", result.TotalCount)
	}

	// Verify links round-trip with team_name.
	links, err := s.Links(ctx, "team-lead-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].TeamName != "work-team" {
		t.Errorf("link TeamName = %q, want %q", links[0].TeamName, "work-team")
	}
	if links[0].Action != "team-spawn" {
		t.Errorf("link Action = %q, want %q", links[0].Action, "team-spawn")
	}
}

func TestUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := cass.Session{
		ID:    "u1",
		Agent: "claude-code",
		Title: "original title",
		Messages: []cass.Message{
			{Role: "user", Content: "original content"},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	// Update same ID with new title.
	sess.Title = "updated title"
	sess.Messages = append(sess.Messages, cass.Message{Role: "assistant", Content: "updated reply"})
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	count, _ := s.SessionCount(ctx)
	if count != 1 {
		t.Errorf("got count %d after upsert, want 1", count)
	}

	// Search should find the updated content.
	result, err := s.Search(ctx, cass.SearchRequest{Query: "updated", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 {
		t.Errorf("got %d results for updated content, want 1", result.TotalCount)
	}
}

func TestGoalsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := cass.Session{
		ID:        "goal-1",
		Agent:     "codex-cli",
		Title:     "Goal session",
		Workspace: "/home/user/project",
		StartedAt: time.Unix(100, 0),
		EndedAt:   time.Unix(200, 0),
		Messages: []cass.Message{
			{Role: "user", Content: "ordinary prompt"},
		},
		Goals: []cass.Goal{
			{
				ThreadID:        "goal-1",
				Objective:       "ship goal support",
				Status:          "complete",
				TokensUsed:      99,
				TimeUsedSeconds: 88,
				UpdatedAt:       time.Unix(190, 0),
				CompletionGates: []cass.GoalGate{
					{Name: "focused trace capture", Status: "missing"},
				},
			},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	result, err := s.Search(ctx, cass.SearchRequest{Query: "goal support", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(result.Hits))
	}
	h := result.Hits[0]
	if h.GoalCount != 1 || h.ActiveGoalCount != 1 || h.CompletedGoalCount != 0 {
		t.Fatalf("goal counts = total %d active %d complete %d", h.GoalCount, h.ActiveGoalCount, h.CompletedGoalCount)
	}
	if len(h.Goals) != 1 || h.Goals[0].Objective != "ship goal support" {
		t.Fatalf("hit goals = %#v", h.Goals)
	}
	if h.Goals[0].EffectiveStatus != "blocked" {
		t.Fatalf("effective status = %q, want blocked", h.Goals[0].EffectiveStatus)
	}
	if len(h.Goals[0].CompletionGates) != 1 || h.Goals[0].CompletionGates[0].Name != "focused trace capture" {
		t.Fatalf("hit goal gates = %#v", h.Goals[0].CompletionGates)
	}

	result, err = s.Search(ctx, cass.SearchRequest{Query: "trace capture", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("gate search hits = %d, want 1", len(result.Hits))
	}

	goals, err := s.Goals(ctx, "complete", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 0 {
		t.Fatalf("complete Goals = %d, want 0", len(goals))
	}

	goals, err = s.Goals(ctx, "blocked", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 {
		t.Fatalf("blocked Goals = %d, want 1", len(goals))
	}
	if goals[0].SessionID != "goal-1" || goals[0].Objective != "ship goal support" {
		t.Fatalf("goal hit = %#v", goals[0])
	}
}

func TestSessionReturnsIndexedMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	sess := cass.Session{
		ID:         "meta-1",
		Agent:      "codex-cli",
		Title:      "Meta session",
		Workspace:  "/work/meta",
		SourcePath: "/tmp/meta.jsonl",
		StartedAt:  time.Unix(100, 0),
		EndedAt:    time.Unix(200, 0),
		Messages: []cass.Message{
			{Role: "user", Content: "show metadata"},
		},
		Goals: []cass.Goal{{
			Objective: "finish metadata",
			Status:    "complete",
			CompletionGates: []cass.GoalGate{
				{Name: "real gate", Status: "missing"},
			},
		}},
		Stats: cass.SessionStats{
			ToolCalls:     3,
			ToolBreakdown: map[string]int{"exec": 2, "read": 1},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchIndexRequests(ctx, []cass.APIRequest{
		{ID: "req-1", SessionID: "meta-1", RequestID: "r1", Timestamp: 150},
		{ID: "req-2", IT2SessionID: "meta-1", RequestID: "r2", Timestamp: 151},
	}); err != nil {
		t.Fatal(err)
	}
	h, err := s.Session(ctx, "meta-1")
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionID != "meta-1" || h.Agent != "codex-cli" || h.Workspace != "/work/meta" {
		t.Fatalf("hit metadata = %#v", h)
	}
	if len(h.Goals) != 1 || h.Goals[0].EffectiveStatus != "blocked" {
		t.Fatalf("hit goals = %#v", h.Goals)
	}
	if h.ToolBreakdown["exec"] != 2 {
		t.Fatalf("tool breakdown = %#v", h.ToolBreakdown)
	}
	if h.APIRequestCount != 2 {
		t.Fatalf("api request count = %d, want 2", h.APIRequestCount)
	}

	result, err := s.Search(ctx, cass.SearchRequest{Query: "metadata", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].APIRequestCount != 2 {
		t.Fatalf("search request count hit = %#v", result.Hits)
	}
}

func TestSessionMappingUsesClaudeSessionID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	claudeSID := "11111111-2222-3333-4444-555555555555"
	itermSID := "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"
	sess := cass.Session{
		ID:         "cass-direct",
		Agent:      "claude-code",
		Title:      "Direct It2 mapping",
		Workspace:  "/work/direct",
		SourcePath: "/tmp/project/" + claudeSID + ".jsonl",
		StartedAt:  time.Unix(100, 0),
		Messages: []cass.Message{
			{ID: "entry-uuid-not-session", Role: "user", Content: "hello"},
		},
		Metadata: map[string]any{"iterm_session": itermSID},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	mappings, err := s.Mappings(ctx, itermSID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 {
		t.Fatalf("mappings = %d, want 1", len(mappings))
	}
	m := mappings[0]
	if m.ClaudeSession != claudeSID {
		t.Fatalf("ClaudeSession = %q, want %q", m.ClaudeSession, claudeSID)
	}
	if m.CASSSession != sess.ID || m.Workspace != sess.Workspace || m.Title != sess.Title {
		t.Fatalf("mapping metadata = %+v, want session metadata", m)
	}
}

func TestBatchIndexRequestsBuildsArtifactMappings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	claudeFirst := "22222222-3333-4444-5555-666666666666"
	itermFirst := "BBBBBBBB-CCCC-DDDD-EEEE-FFFFFFFFFFFF"
	firstSess := cass.Session{
		ID:         "cass-first",
		Agent:      "claude-code",
		Title:      "Session before artifacts",
		Workspace:  "/work/first",
		SourcePath: "/tmp/project/" + claudeFirst + ".jsonl",
		StartedAt:  time.Unix(200, 0),
	}
	if err := s.BatchIndex(ctx, []cass.Session{firstSess}); err != nil {
		t.Fatal(err)
	}
	if err := s.BatchIndexRequests(ctx, []cass.APIRequest{{
		ID:                "req-first",
		SessionID:         claudeFirst,
		RequestID:         "r-first",
		Timestamp:         210,
		IT2SessionID:      itermFirst,
		ClientPID:         4242,
		InputTokens:       10,
		OutputTokens:      2,
		SourceHash:        "req-first",
		Model:             "claude-opus-4-6",
		ModelFamily:       "opus",
		TotalRequestBytes: 1,
	}}); err != nil {
		t.Fatal(err)
	}

	mappings, err := s.Mappings(ctx, itermFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 {
		t.Fatalf("first-order mappings = %d, want 1", len(mappings))
	}
	if m := mappings[0]; m.ClaudeSession != claudeFirst || m.CASSSession != firstSess.ID || m.Workspace != firstSess.Workspace {
		t.Fatalf("first-order mapping = %+v", m)
	}

	reqs, err := s.QueryRequests(ctx, firstSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0].SessionID != claudeFirst || reqs[0].IT2SessionID != itermFirst {
		t.Fatalf("QueryRequests via mapping = %+v, want artifact request", reqs)
	}
	hit, err := s.Session(ctx, firstSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hit.APIRequestCount != 1 {
		t.Fatalf("APIRequestCount = %d, want 1", hit.APIRequestCount)
	}

	claudeLater := "33333333-4444-5555-6666-777777777777"
	itermLater := "CCCCCCCC-DDDD-EEEE-FFFF-000000000000"
	if err := s.BatchIndexRequests(ctx, []cass.APIRequest{{
		ID:           "req-later",
		SessionID:    claudeLater,
		RequestID:    "r-later",
		Timestamp:    310,
		IT2SessionID: itermLater,
		ClientPID:    4343,
		SourceHash:   "req-later",
	}}); err != nil {
		t.Fatal(err)
	}
	mappings, err = s.Mappings(ctx, itermLater)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].ClaudeSession != claudeLater || mappings[0].CASSSession != "" {
		t.Fatalf("request-before-session mapping = %+v, want claude-only row", mappings)
	}

	laterSess := cass.Session{
		ID:         "cass-later",
		Agent:      "claude-code",
		Title:      "Session after artifacts",
		Workspace:  "/work/later",
		SourcePath: "/tmp/project/" + claudeLater + ".jsonl",
		StartedAt:  time.Unix(300, 0),
	}
	if err := s.BatchIndex(ctx, []cass.Session{laterSess}); err != nil {
		t.Fatal(err)
	}
	mappings, err = s.Mappings(ctx, itermLater)
	if err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 {
		t.Fatalf("backfilled mappings = %d, want 1", len(mappings))
	}
	if m := mappings[0]; m.ClaudeSession != claudeLater || m.CASSSession != laterSess.ID || m.Workspace != laterSess.Workspace {
		t.Fatalf("backfilled mapping = %+v", m)
	}
}

func TestSkillsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	sess := cass.Session{
		ID:        "skill-session",
		Agent:     "claude-code",
		Title:     "Use notebook skill",
		Workspace: "/tmp/project",
		StartedAt: now.Add(-time.Hour),
		EndedAt:   now,
		Messages:  []cass.Message{{Role: "user", Content: "use the notebook skill"}},
		Skills: []cass.SkillUse{
			{
				Name:      "notebooklm-assisted-prompting",
				Path:      "/Users/tmc/go/src/github.com/tmc/skills/skills/notebooklm-assisted-prompting/SKILL.md",
				Source:    "claude-code",
				Kind:      "loaded",
				Count:     1,
				FirstSeen: now.Add(-30 * time.Minute),
				LastSeen:  now.Add(-30 * time.Minute),
				Evidence:  []string{"Read tool loaded SKILL.md"},
			},
			{
				Name:      "nlm",
				Source:    "claude-code",
				Kind:      "selected",
				Count:     1,
				FirstSeen: now.Add(-20 * time.Minute),
				LastSeen:  now.Add(-20 * time.Minute),
				Evidence:  []string{"Skill tool invocation"},
			},
			{
				Name:      "imagegen",
				Source:    "codex-cli",
				Kind:      "available",
				Count:     1,
				FirstSeen: now.Add(-10 * time.Minute),
				LastSeen:  now.Add(-10 * time.Minute),
				Evidence:  []string{"available skills list"},
			},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	result, err := s.Search(ctx, cass.SearchRequest{
		Query:   "skill",
		Limit:   10,
		Filters: cass.Filters{Skill: "nlm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.TotalCount != 1 {
		t.Fatalf("TotalCount = %d, want 1", result.TotalCount)
	}
	h := result.Hits[0]
	if h.SkillCount != 3 || h.SelectedSkillCount != 1 || h.LoadedSkillCount != 1 {
		t.Fatalf("skill counts = %d/%d/%d, want 3/1/1", h.SkillCount, h.SelectedSkillCount, h.LoadedSkillCount)
	}
	if len(h.Skills) != 3 {
		t.Fatalf("len(hit.Skills) = %d, want 3", len(h.Skills))
	}

	skills, err := s.Skills(ctx, "nlm", "selected", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "nlm" || skills[0].SessionID != "skill-session" {
		t.Fatalf("Skills = %+v, want selected nlm hit", skills)
	}

	agg, err := s.AggregateStats(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if agg["skills"].(int) != 3 || agg["selected_skills"].(int) != 1 || agg["loaded_skills"].(int) != 1 {
		t.Fatalf("aggregate skill counts = %#v", agg)
	}
	top := agg["top_skills"].(map[string]int)
	if top["nlm"] != 1 || top["notebooklm-assisted-prompting"] != 1 {
		t.Fatalf("top skills = %#v", top)
	}
	if top["imagegen"] != 0 {
		t.Fatalf("top skills include available-only imagegen: %#v", top)
	}
}

// TestBatchIndexSubagentRuns verifies that SubagentRun records persist
// alongside their parent session, and that re-indexing the same session
// with a different set of runs replaces them rather than duplicating.
func TestBatchIndexSubagentRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SaveAgentDef(ctx, AgentDef{
		Name:        "reviewer",
		Description: "review code changes",
		SourcePath:  "/tmp/agents/reviewer.json",
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().Add(-time.Hour)
	sess := cass.Session{
		ID:        "parent1",
		Agent:     "claude-code",
		Title:     "with subagents",
		Workspace: "/tmp/proj",
		StartedAt: t0,
		EndedAt:   t0.Add(30 * time.Minute),
		Subagents: []cass.SubagentRun{
			{
				AgentID:         "agA",
				ParentSessionID: "parent1",
				ParentClaudeSID: "claude-uuid",
				Workspace:       "/tmp/proj",
				AgentType:       "general-purpose",
				Description:     "do A",
				Model:           "claude-haiku-4-5",
				EnqueuedAt:      t0.Add(time.Minute),
				DequeuedAt:      t0.Add(2 * time.Minute),
				StartedAt:       t0.Add(time.Minute),
				EndedAt:         t0.Add(90 * time.Second),
				Status:          "completed",
				ToolUseID:       "toolu_a",
				TotalTokens:     1234,
				ToolUses:        5,
				DurationMs:      9000,
				EntryCount:      4,
				SourcePath:      "/tmp/proj/parent1/subagents/agent-agA.jsonl",
			},
			{
				AgentID:         "agB",
				ParentSessionID: "parent1",
				Workspace:       "/tmp/proj",
				AgentType:       "reviewer",
				Status:          "unknown",
				StartedAt:       t0.Add(3 * time.Minute),
				EndedAt:         t0.Add(4 * time.Minute),
				EntryCount:      2,
			},
			{
				AgentID:         "acompact-agA",
				ParentSessionID: "parent1",
				Workspace:       "/tmp/proj",
				Status:          "unknown",
				StartedAt:       t0.Add(5 * time.Minute),
				EndedAt:         t0.Add(6 * time.Minute),
				EntryCount:      1,
				IsCompaction:    true,
			},
		},
	}

	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagent_runs WHERE parent_session_id = ?", sess.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("subagent_runs count = %d, want 3", count)
	}

	// Verify denorm counter.
	var denormCount int
	if err := s.db.QueryRowContext(ctx, "SELECT subagent_run_count FROM sessions WHERE id = ?", sess.ID).Scan(&denormCount); err != nil {
		t.Fatal(err)
	}
	if denormCount != 3 {
		t.Errorf("sessions.subagent_run_count = %d, want 3", denormCount)
	}

	// Spot-check one row.
	var (
		gotStatus, gotModel, gotAgentType string
		gotTotalTokens, gotToolUses       int
		gotDurationMs                     int64
	)
	if err := s.db.QueryRowContext(ctx, `
		SELECT status, model, agent_type, total_tokens, tool_uses, duration_ms
		FROM subagent_runs WHERE parent_session_id = ? AND agent_id = ?
	`, sess.ID, "agA").Scan(&gotStatus, &gotModel, &gotAgentType, &gotTotalTokens, &gotToolUses, &gotDurationMs); err != nil {
		t.Fatal(err)
	}
	if gotStatus != "completed" || gotModel != "claude-haiku-4-5" || gotAgentType != "general-purpose" {
		t.Errorf("agA scalars: status=%q model=%q type=%q", gotStatus, gotModel, gotAgentType)
	}
	if gotTotalTokens != 1234 || gotToolUses != 5 || gotDurationMs != 9000 {
		t.Errorf("agA usage: tokens=%d toolUses=%d ms=%d", gotTotalTokens, gotToolUses, gotDurationMs)
	}

	var gotCompaction int
	if err := s.db.QueryRowContext(ctx, `
		SELECT is_compaction FROM subagent_runs WHERE parent_session_id = ? AND agent_id = ?
	`, sess.ID, "acompact-agA").Scan(&gotCompaction); err != nil {
		t.Fatal(err)
	}
	if gotCompaction != 1 {
		t.Errorf("acompact is_compaction = %d, want 1", gotCompaction)
	}
	graphRuns, err := s.GraphSubagents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range graphRuns {
		if r.AgentID == "acompact-agA" {
			t.Fatalf("GraphSubagents included compaction run: %+v", r)
		}
	}
	listRuns, err := s.SubagentRuns(ctx, SubagentRunFilter{AgentType: "reviewer", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(listRuns) != 1 {
		t.Fatalf("SubagentRuns reviewer = %d, want 1", len(listRuns))
	}
	if listRuns[0].AgentDefName != "reviewer" || listRuns[0].AgentDefDescription != "review code changes" {
		t.Fatalf("SubagentRuns agent def link = %+v, want reviewer definition", listRuns[0])
	}
	if listRuns[0].AgentDefSourcePath != "/tmp/agents/reviewer.json" {
		t.Fatalf("AgentDefSourcePath = %q, want /tmp/agents/reviewer.json", listRuns[0].AgentDefSourcePath)
	}
	foundReviewerGraph := false
	for _, r := range graphRuns {
		if r.AgentID == "agB" {
			foundReviewerGraph = true
			if r.AgentDefName != "reviewer" || r.AgentDefDescription != "review code changes" {
				t.Fatalf("GraphSubagents agent def link = %+v, want reviewer definition", r)
			}
		}
	}
	if !foundReviewerGraph {
		t.Fatalf("GraphSubagents missing agB")
	}

	// Re-index with a different set of runs (drop agA, keep agB, add agC).
	sess.Subagents = []cass.SubagentRun{
		sess.Subagents[1],
		{
			AgentID:         "agC",
			ParentSessionID: "parent1",
			Workspace:       "/tmp/proj",
			Status:          "completed",
			TotalTokens:     500,
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	// agA must be gone, agB and agC present.
	rows, err := s.db.QueryContext(ctx, "SELECT agent_id FROM subagent_runs WHERE parent_session_id = ? ORDER BY agent_id", sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	want := []string{"agB", "agC"}
	if len(ids) != len(want) {
		t.Fatalf("after re-index: got ids %v, want %v", ids, want)
	}
	for i := range ids {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}

	// Denorm counter should reflect the new count.
	if err := s.db.QueryRowContext(ctx, "SELECT subagent_run_count FROM sessions WHERE id = ?", sess.ID).Scan(&denormCount); err != nil {
		t.Fatal(err)
	}
	if denormCount != 2 {
		t.Errorf("after re-index: subagent_run_count = %d, want 2", denormCount)
	}
}

// TestSubagentRunsRemovedOnSessionDelete verifies that deleting a parent
// session also removes its subagent_runs rows. We do not rely on FK
// cascade (SQLite foreign_keys pragma is off by default per connection);
// Delete explicitly clears subagent_runs before deleting the session.
func TestSubagentRunsRemovedOnSessionDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := cass.Session{
		ID:        "casc1",
		Agent:     "claude-code",
		Title:     "to delete",
		StartedAt: time.Now(),
		Subagents: []cass.SubagentRun{
			{AgentID: "x", ParentSessionID: "casc1", Status: "completed"},
			{AgentID: "y", ParentSessionID: "casc1", Status: "completed"},
		},
	}
	if err := s.BatchIndex(ctx, []cass.Session{sess}); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagent_runs WHERE parent_session_id = ?", "casc1").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 2 {
		t.Fatalf("before delete: subagent_runs count = %d, want 2", before)
	}

	if err := s.Delete(ctx, cass.DeleteFilter{IDs: []string{"casc1"}}); err != nil {
		t.Fatal(err)
	}

	var after int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagent_runs WHERE parent_session_id = ?", "casc1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Errorf("after delete: subagent_runs count = %d, want 0", after)
	}
}

// TestSubagentRunsRemovedOnAgentDelete verifies the agent-filter delete
// path also removes subagent_runs for the matched sessions and leaves
// other agents' rows untouched.
func TestSubagentRunsRemovedOnAgentDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.BatchIndex(ctx, []cass.Session{
		{
			ID: "a1", Agent: "claude-code", Title: "x", StartedAt: time.Now(),
			Subagents: []cass.SubagentRun{{AgentID: "ra", ParentSessionID: "a1"}},
		},
		{
			ID: "a2", Agent: "cursor", Title: "y", StartedAt: time.Now(),
			Subagents: []cass.SubagentRun{{AgentID: "rb", ParentSessionID: "a2"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, cass.DeleteFilter{Agent: "claude-code"}); err != nil {
		t.Fatal(err)
	}

	var ccCount, cursorCount int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagent_runs WHERE parent_session_id = ?", "a1").Scan(&ccCount); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subagent_runs WHERE parent_session_id = ?", "a2").Scan(&cursorCount); err != nil {
		t.Fatal(err)
	}
	if ccCount != 0 {
		t.Errorf("claude-code subagent_runs after delete: %d, want 0", ccCount)
	}
	if cursorCount != 1 {
		t.Errorf("cursor subagent_runs after delete (should be untouched): %d, want 1", cursorCount)
	}
}

// TestResolveLabelsManyPrefixes guards against the SQLite expression-depth
// overflow (SQLITE_MAX_EXPR_DEPTH, default 1000) that a single chained
// "LIKE ? OR LIKE ? OR ..." hit once a busy graph produced thousands of unique
// session prefixes — it returned "Expression tree is too large" and 500'd the
// legacy workflow=none graph. ResolveLabels now batches the query.
func TestResolveLabelsManyPrefixes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const n = 2500 // comfortably over the 1000-deep OR limit and the 500 batch size.
	prefixes := make([]string, n)
	for i := range prefixes {
		sid := fmt.Sprintf("sess-%05d-full-iterm-id", i)
		prefixes[i] = fmt.Sprintf("sess-%05d", i) // the prefix the graph collects.
		if err := s.SaveMapping(ctx, sid, "", fmt.Sprintf("cass-%05d", i),
			fmt.Sprintf("/ws/%d", i), fmt.Sprintf("title %d", i), 0); err != nil {
			t.Fatalf("SaveMapping %d: %v", i, err)
		}
	}

	labels, err := s.ResolveLabels(ctx, prefixes)
	if err != nil {
		t.Fatalf("ResolveLabels(%d prefixes): %v", n, err)
	}
	if len(labels) != n {
		t.Fatalf("resolved %d labels, want %d", len(labels), n)
	}
	// Spot-check a label from each batch boundary resolves to the right row.
	for _, i := range []int{0, 499, 500, 999, 1000, 2499} {
		p := fmt.Sprintf("sess-%05d", i)
		l, ok := labels[p]
		if !ok {
			t.Fatalf("prefix %q not resolved", p)
		}
		if want := fmt.Sprintf("/ws/%d", i); l.Workspace != want {
			t.Errorf("prefix %q workspace = %q, want %q", p, l.Workspace, want)
		}
	}
}

// TestPurgeSubagentSessions verifies the one-time cleanup removes session rows
// whose source_path lies under a subagents/ segment (transcripts that older
// indexing wrongly emitted as standalone sessions) while leaving real sessions
// and their FTS rows intact.
func TestPurgeSubagentSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sessions := []cass.Session{
		{ID: "real1", Agent: "claude-code", Title: "real session",
			SourcePath: "/u/.claude/projects/-w/real1.jsonl",
			Messages:   []cass.Message{{Role: "user", Content: "legitimate work"}}},
		{ID: "wfagent1", Agent: "claude-code", Title: "leaked workflow agent",
			SourcePath: "/u/.claude/projects/-w/real1/subagents/workflows/wf_x/agent-a.jsonl",
			Messages:   []cass.Message{{Role: "user", Content: "agent transcript text"}}},
		{ID: "subagent1", Agent: "claude-code", Title: "leaked plain subagent",
			SourcePath: "/u/.claude/projects/-w/real1/subagents/agent-b.jsonl",
			Messages:   []cass.Message{{Role: "user", Content: "subagent transcript text"}}},
	}
	if err := s.BatchIndex(ctx, sessions); err != nil {
		t.Fatal(err)
	}

	if err := s.purgeSubagentSessions(ctx); err != nil {
		t.Fatalf("purgeSubagentSessions: %v", err)
	}

	// Only the real session remains.
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("sessions after purge = %d, want 1", n)
	}
	var leaked int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions WHERE source_path LIKE '%/subagents/%'`).Scan(&leaked)
	if leaked != 0 {
		t.Fatalf("subagent rows after purge = %d, want 0", leaked)
	}
	// FTS row for the purged content is gone; the real one is searchable.
	res, err := s.Search(ctx, cass.SearchRequest{Query: "transcript", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("purged agent text still searchable: %d hits", len(res.Hits))
	}
	res, err = s.Search(ctx, cass.SearchRequest{Query: "legitimate", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].SessionID != "real1" {
		t.Errorf("real session not searchable after purge: %+v", res.Hits)
	}

	// Idempotent: a second purge is a no-op.
	if err := s.purgeSubagentSessions(ctx); err != nil {
		t.Fatalf("second purge: %v", err)
	}
}
