package store

import (
	"context"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func newTestStore(t *testing.T) *Store {
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
				{Role: "assistant", Content: "here's the SQL migration"},
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
	v, err := s.GetMeta(ctx, "schema_version")
	if err != nil {
		t.Fatal(err)
	}
	if v != "1" {
		t.Errorf("got %q, want %q", v, "1")
	}

	// Missing key returns empty string.
	v, err = s.GetMeta(ctx, "nonexistent")
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
