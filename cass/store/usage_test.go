package store

import (
	"context"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/ccusage"
)

// usageAt builds one usage event at the given time.
func usageAt(ts time.Time, agent, model, repo string, in, read, create, out int) cass.UsageEvent {
	return cass.UsageEvent{
		Record: ccusage.Record{
			Agent: agent, Time: ts, Model: model, Vendor: ccusage.Vendor(model),
			Input: in, CacheRead: read, CacheCreate: create, Output: out,
		},
		GitCommonDir: repo,
	}
}

// usageSessions is the fixture the query tests share: two agents, two repos,
// one unattributed session, spread over three days.
func usageSessions() []cass.Session {
	day := func(d, h int) time.Time {
		return time.Date(2026, 6, d, h, 30, 0, 0, time.UTC)
	}
	return []cass.Session{
		{
			ID: "s1", Agent: "claude-code", StartedAt: day(1, 0), EndedAt: day(1, 23),
			Usage: []cass.UsageEvent{
				usageAt(day(1, 9), "claude-code", "claude-opus-5", "/r/a/.git", 10, 100, 20, 50),
				usageAt(day(1, 10), "claude-code", "claude-opus-5", "/r/a/.git", 5, 200, 0, 30),
				usageAt(day(3, 9), "claude-code", "claude-haiku-4-5", "/r/a/.git", 1, 2, 3, 4),
			},
		},
		{
			ID: "s2", Agent: "codex-cli", StartedAt: day(2, 0), EndedAt: day(2, 5),
			Usage: []cass.UsageEvent{
				usageAt(day(2, 14), "codex-cli", "gpt-5.4-codex", "/r/b/.git", 7, 0, 0, 11),
			},
		},
		{
			ID: "s3", Agent: "codex-cli", StartedAt: day(2, 0), EndedAt: day(2, 1),
			Usage: []cass.UsageEvent{
				usageAt(day(2, 15), "codex-cli", "", "", 100, 0, 0, 1),
			},
		},
	}
}

func indexUsage(t *testing.T, s *DB) context.Context {
	t.Helper()
	ctx := context.Background()
	if err := s.BatchIndex(ctx, usageSessions()); err != nil {
		t.Fatal(err)
	}
	return ctx
}

// TestUsageEventsRoundTrip checks that every event a session carries reaches
// the table with its counts and dimensions intact.
func TestUsageEventsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	count, oldest, err := s.UsageEventCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("usage rows = %d, want 5", count)
	}
	if want := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC); !oldest.Equal(want) {
		t.Errorf("oldest = %v, want %v", oldest, want)
	}

	groups, err := s.UsageByGroup(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 4 {
		t.Fatalf("groups = %d, want 4 (three models plus the unattributed one)", len(groups))
	}
	top := groups[0]
	if top.Repo != "/r/a/.git" || top.Model != "claude-opus-5" {
		t.Errorf("top group = %s %s, want /r/a/.git claude-opus-5", top.Repo, top.Model)
	}
	if top.Input != 15 || top.CacheRead != 300 || top.CacheCreate != 20 || top.Output != 80 {
		t.Errorf("top group tokens = %+v, want 15/300/20/80", top)
	}
	if top.Sessions != 1 || top.Requests != 2 {
		t.Errorf("top group = %d sessions, %d requests; want 1, 2", top.Sessions, top.Requests)
	}
	if top.Vendor != "anthropic" {
		t.Errorf("top group vendor = %q, want anthropic", top.Vendor)
	}
}

// TestUsageReindexIsIdempotent checks the delete-and-reinsert write path: a
// session indexed twice holds one copy of its rows, and a session whose usage
// shrinks does not keep the rows it lost.
func TestUsageReindexIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)
	if err := s.BatchIndex(ctx, usageSessions()); err != nil {
		t.Fatal(err)
	}
	count, _, err := s.UsageEventCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Errorf("usage rows after reindex = %d, want 5", count)
	}

	shrunk := usageSessions()
	shrunk[0].Usage = shrunk[0].Usage[:1]
	if err := s.BatchIndex(ctx, shrunk[:1]); err != nil {
		t.Fatal(err)
	}
	if count, _, _ := s.UsageEventCount(ctx); count != 3 {
		t.Errorf("usage rows after shrinking s1 = %d, want 3", count)
	}
}

// TestUsageViewsSumAlike checks the invariant every view rests on: grouping by
// repository, by provider, or by hour must reach the same four token totals
// for one filter. Unattributed rows are part of that sum, not an exclusion.
func TestUsageViewsSumAlike(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	type totals struct{ in, read, create, out, reqs int }
	groups, err := s.UsageByGroup(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var byGroup, byRepo, byHour totals
	for _, g := range groups {
		byGroup.in += g.Input
		byGroup.read += g.CacheRead
		byGroup.create += g.CacheCreate
		byGroup.out += g.Output
		byGroup.reqs += g.Requests
		if g.Repo == "" {
			continue
		}
		byRepo.in += g.Input
	}
	hours, err := s.UsageByHour(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hours {
		byHour.in += h.Input
		byHour.read += h.CacheRead
		byHour.create += h.CacheCreate
		byHour.out += h.Output
		byHour.reqs += h.Requests
	}
	if byGroup != byHour {
		t.Errorf("group totals %+v != hour totals %+v", byGroup, byHour)
	}
	if want := (totals{in: 123, read: 302, create: 23, out: 96, reqs: 5}); byGroup != want {
		t.Errorf("totals = %+v, want %+v", byGroup, want)
	}
	if byRepo.in == byGroup.in {
		t.Error("the unattributed session contributed nothing; it must be a visible bucket, not a dropped row")
	}
}

// TestUsageFilters checks each filter dimension independently.
func TestUsageFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	tests := []struct {
		name   string
		filter UsageFilter
		want   int // requests
	}{
		{"all", UsageFilter{}, 5},
		{"agent", UsageFilter{Agent: "codex-cli"}, 2},
		{"repo", UsageFilter{Repo: "/r/a/.git"}, 3},
		{"unattributed", UsageFilter{Repo: "-"}, 0},
		{"after", UsageFilter{After: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}, 3},
		{"before", UsageFilter{Before: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}, 2},
		{"window", UsageFilter{
			After:  time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
			Before: time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC),
		}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours, err := s.UsageByHour(ctx, tt.filter)
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for _, h := range hours {
				got += h.Requests
			}
			if got != tt.want {
				t.Errorf("requests = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestUsageByHourBucketsUTC checks that rows land in the UTC hour of their own
// timestamp. Reports map the hour into a display zone themselves, so an hour
// that is wrong here is wrong in every view.
func TestUsageByHourBucketsUTC(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	hours, err := s.UsageByHour(ctx, UsageFilter{Agent: "claude-code"})
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, h := range hours {
		got = append(got, h.Hour*3600)
	}
	want := []int64{
		time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC).Unix(),
		time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC).Unix(),
	}
	if len(got) != len(want) {
		t.Fatalf("hours = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hour[%d] = %v, want %v", i, time.Unix(got[i], 0).UTC(), time.Unix(want[i], 0).UTC())
		}
	}
}

// TestUsageSessions checks the per-session spans the report header is built
// from: one row per session, covering only the part inside the window.
func TestUsageSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	rows, err := s.UsageSessions(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("sessions = %d, want 3", len(rows))
	}
	if rows[0].SessionID != "s1" || rows[0].Requests != 3 {
		t.Errorf("first session = %s with %d requests, want s1 with 3", rows[0].SessionID, rows[0].Requests)
	}
	// s1 spans June 1 09:30 to June 3 09:30.
	if want := 48 * time.Hour; rows[0].Duration() != want {
		t.Errorf("s1 duration = %v, want %v", rows[0].Duration(), want)
	}

	windowed, err := s.UsageSessions(ctx, UsageFilter{
		Before: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(windowed) != 1 || windowed[0].Duration() != time.Hour {
		t.Errorf("windowed = %+v, want one session spanning an hour", windowed)
	}
}

// TestUsageEventsFollowSessionDelete checks that usage rows go with their
// session, so a deleted session leaves no tokens behind in any total.
func TestUsageEventsFollowSessionDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	if err := s.Delete(ctx, cass.DeleteFilter{IDs: []string{"s1"}}); err != nil {
		t.Fatal(err)
	}
	count, _, err := s.UsageEventCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("usage rows after deleting s1 = %d, want 2", count)
	}
}

// TestUsageMissingTimestampFallsBackToSessionStart checks that a harness with
// no per-request timestamp lands on the session's day rather than at the far
// edge of the unix epoch.
func TestUsageMissingTimestampFallsBackToSessionStart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	start := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	err := s.BatchIndex(ctx, []cass.Session{{
		ID: "s9", Agent: "opencode", StartedAt: start, EndedAt: start,
		Usage: []cass.UsageEvent{{Record: ccusage.Record{Input: 5, Output: 1}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	hours, err := s.UsageByHour(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 1 || hours[0].Hour != start.Unix()/3600 {
		t.Errorf("hours = %+v, want one row at the session start hour", hours)
	}
}

// TestReposRoundTrip checks the repository dimension table.
func TestReposRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	want := []Repo{
		{GitCommonDir: "/r/a/.git", Remote: "github.com/tmc/cc", Name: "github.com/tmc/cc", ResolvedAt: 100},
		{GitCommonDir: "/r/b/.git", Name: "b"},
		{GitCommonDir: ""}, // skipped: a repo with no key is not a repo
	}
	if err := s.SaveRepos(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("repos = %d, want 2", len(got))
	}
	if got["/r/a/.git"] != want[0] {
		t.Errorf("repo = %+v, want %+v", got["/r/a/.git"], want[0])
	}
}

// TestWorktreePaths checks the checkout map prefix attribution is built from.
func TestWorktreePaths(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	err := s.BatchIndex(ctx, []cass.Session{
		{ID: "a", Workspace: "/w/one", GitCommonDir: "/w/one/.git"},
		{ID: "b", Workspace: "/w/two", GitCommonDir: ""},
		{ID: "c", Workspace: "", GitCommonDir: "/w/three/.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := s.WorktreePaths(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths["/w/one"] != "/w/one/.git" {
		t.Errorf("worktree paths = %v, want only the session with both", paths)
	}
}

// TestUsageBySession checks the per-session ranking: one row per session,
// largest first, and that the Session filter narrows to one of them.
func TestUsageBySession(t *testing.T) {
	s := newTestStore(t)
	ctx := indexUsage(t, s)

	rows, err := s.UsageBySession(ctx, UsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	// s1 totals 425 tokens, s3 101, s2 18.
	want := []string{"s1", "s3", "s2"}
	for i, w := range want {
		if rows[i].SessionID != w {
			t.Errorf("row %d = %s, want %s", i, rows[i].SessionID, w)
		}
	}
	if got := rows[0]; got.Requests != 3 || got.Input != 16 || got.CacheRead != 302 ||
		got.CacheCreate != 23 || got.Output != 84 || got.Repo != "/r/a/.git" || got.Agent != "claude-code" {
		t.Errorf("s1 = %+v", got)
	}

	one, err := s.UsageBySession(ctx, UsageFilter{Session: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].SessionID != "s2" || one[0].Output != 11 {
		t.Errorf("filtered = %+v, want just s2", one)
	}
}
