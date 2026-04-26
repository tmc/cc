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

// TestBatchIndexSubagentRuns verifies that SubagentRun records persist
// alongside their parent session, and that re-indexing the same session
// with a different set of runs replaces them rather than duplicating.
func TestBatchIndexSubagentRuns(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

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
				Status:          "unknown",
				StartedAt:       t0.Add(3 * time.Minute),
				EndedAt:         t0.Add(4 * time.Minute),
				EntryCount:      2,
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
	if count != 2 {
		t.Fatalf("subagent_runs count = %d, want 2", count)
	}

	// Verify denorm counter.
	var denormCount int
	if err := s.db.QueryRowContext(ctx, "SELECT subagent_run_count FROM sessions WHERE id = ?", sess.ID).Scan(&denormCount); err != nil {
		t.Fatal(err)
	}
	if denormCount != 2 {
		t.Errorf("sessions.subagent_run_count = %d, want 2", denormCount)
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
