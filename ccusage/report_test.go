package ccusage

import (
	"testing"
	"time"
)

var la = mustLoad("America/Los_Angeles")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// hourAt builds an Hour for the UTC hour containing t, carrying n total tokens
// split so that every field is exercised.
func hourAt(t time.Time, n int) Hour {
	return Hour{
		Hour: t.UTC().Unix() / 3600, Requests: 1,
		Tokens: Tokens{Input: n / 4, CacheRead: n / 4, CacheCreate: n / 4, Output: n - 3*(n/4)},
	}
}

// window returns the window of the days [start, start+days) in loc.
func window(start time.Time, days int, loc *time.Location) Window {
	s := start.In(loc)
	s = time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
	return Window{Start: s, End: s.AddDate(0, 0, days), Loc: loc}
}

// TestDaysCoverWindow checks that every calendar day in the window appears,
// including the empty ones. The weekday mean divides by them, so a missing
// zero day silently inflates the average.
func TestDaysCoverWindow(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 14, la)
	hours := []Hour{hourAt(time.Date(2026, 6, 3, 10, 0, 0, 0, la), 400)}
	days := Days(hours, w)
	if len(days) != 14 {
		t.Fatalf("got %d days, want 14", len(days))
	}
	active := 0
	for _, d := range days {
		if d.Requests > 0 {
			active++
			if !d.Date.Equal(time.Date(2026, 6, 3, 0, 0, 0, 0, la)) {
				t.Errorf("active day = %s, want 2026-06-03", d.Date)
			}
		}
	}
	if active != 1 {
		t.Errorf("got %d active days, want 1", active)
	}
}

// TestDaysAcrossDST checks the local-day boundary on both 2026 transitions and
// in UTC. Bucketing by a fixed 24-hour stride moves the requests on either
// side of a transition into the wrong day.
func TestDaysAcrossDST(t *testing.T) {
	tests := []struct {
		name    string
		loc     *time.Location
		day     time.Time // the transition day, local
		lateUTC time.Time // 23:30 UTC the day before, in local terms
	}{
		{"spring forward", la, time.Date(2026, 3, 8, 0, 0, 0, 0, la), time.Date(2026, 3, 7, 23, 30, 0, 0, time.UTC)},
		{"fall back", la, time.Date(2026, 11, 1, 0, 0, 0, 0, la), time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)},
		{"utc", time.UTC, time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 7, 23, 30, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := window(tt.day.AddDate(0, 0, -2), 5, tt.loc)
			hours := []Hour{
				hourAt(tt.lateUTC, 100),
				hourAt(tt.lateUTC.Add(time.Hour), 100),
			}
			days := Days(hours, w)
			total := 0
			for _, d := range days {
				total += d.Total()
				local := time.Unix(hours[0].Hour*3600, 0).In(tt.loc)
				if d.Requests > 0 && d.Date.Location() != tt.loc {
					t.Errorf("day %s not in %s", d.Date, tt.loc)
				}
				_ = local
			}
			if total != 200 {
				t.Errorf("total = %d, want 200: a request fell outside the window", total)
			}
		})
	}
}

// TestWeekdayMeanCountsEmptyDays checks the divisor: a window with two
// Sundays, one of them idle, halves the Sunday mean.
func TestWeekdayMeanCountsEmptyDays(t *testing.T) {
	// 2026-06-01 is a Monday, so a 14-day window holds two of each weekday.
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 14, la)
	hours := []Hour{hourAt(time.Date(2026, 6, 7, 12, 0, 0, 0, la), 1000)} // The first Sunday.
	rows := Weekdays(Days(hours, w), "total", DefaultPricing)
	var sun WeekdayRow
	for _, r := range rows {
		if r.Weekday == time.Sunday {
			sun = r
		}
	}
	if sun.Count != 2 {
		t.Fatalf("Sunday count = %d, want 2", sun.Count)
	}
	if sun.Active != 1 {
		t.Errorf("Sunday active = %d, want 1", sun.Active)
	}
	if sun.Mean != 500 {
		t.Errorf("Sunday mean = %v, want 500 (1000 over two Sundays)", sun.Mean)
	}
	if sun.Median != 500 {
		t.Errorf("Sunday median = %v, want 500 (the mean of 0 and 1000)", sun.Median)
	}
	if rows[0].Weekday != time.Monday {
		t.Errorf("first row = %s, want Monday", rows[0].Weekday)
	}
}

// TestStreaks checks the current and longest runs, including that the current
// streak is zero when the last day of the window is idle.
func TestStreaks(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 7, la)
	var hours []Hour
	for _, day := range []int{1, 2, 3, 6} { // A run of three, then a gap, then one.
		hours = append(hours, hourAt(time.Date(2026, 6, day, 12, 0, 0, 0, la), 100))
	}
	days := Days(hours, w)
	cur, longest := streaks(days)
	if longest != 3 {
		t.Errorf("longest = %d, want 3", longest)
	}
	if cur != 0 {
		t.Errorf("current = %d, want 0: 2026-06-07 is idle", cur)
	}
}

// TestCalendarLevels checks that levels are quantiles of the active days, so
// one enormous day does not push every other day to the lowest level.
func TestCalendarLevels(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 8, la)
	sizes := []int{100, 200, 300, 400, 500, 600, 700, 1_000_000}
	var hours []Hour
	for i, n := range sizes {
		hours = append(hours, hourAt(time.Date(2026, 6, 1+i, 12, 0, 0, 0, la), n))
	}
	cal := Calendar(Days(hours, w), "total", DefaultPricing)
	levels := make([]int, len(cal))
	for i, c := range cal {
		levels[i] = c.Level
	}
	if levels[len(levels)-1] != 4 {
		t.Errorf("largest day level = %d, want 4", levels[len(levels)-1])
	}
	if levels[0] != 1 {
		t.Errorf("smallest day level = %d, want 1", levels[0])
	}
	seen := map[int]bool{}
	for _, l := range levels {
		seen[l] = true
	}
	for l := 1; l <= 4; l++ {
		if !seen[l] {
			t.Errorf("level %d unused; a fixed-fraction scale would collapse the small days", l)
		}
	}
}

// TestCalendarZeroDayIsLevelZero checks the "·" cell stays distinct from the
// lowest quartile.
func TestCalendarZeroDayIsLevelZero(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 3, la)
	hours := []Hour{hourAt(time.Date(2026, 6, 2, 12, 0, 0, 0, la), 100)}
	for _, c := range Calendar(Days(hours, w), "total", DefaultPricing) {
		want := 0
		if c.Requests > 0 {
			want = 4
		}
		if c.Level != want {
			t.Errorf("%s level = %d, want %d", c.Date.Format("01-02"), c.Level, want)
		}
	}
}

// TestBucketsWeekAndCumulative checks that week buckets start on Monday and
// that -cumulative reports a running total rather than a per-bucket sum.
func TestBucketsWeekAndCumulative(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 14, la)
	var hours []Hour
	for i := 0; i < 14; i++ {
		hours = append(hours, hourAt(time.Date(2026, 6, 1+i, 12, 0, 0, 0, la), 100))
	}
	days := Days(hours, w)

	weeks := Buckets(days, "week", false)
	if len(weeks) != 2 {
		t.Fatalf("got %d weeks, want 2", len(weeks))
	}
	for _, b := range weeks {
		if b.Start.Weekday() != time.Monday {
			t.Errorf("week starts on %s, want Monday", b.Start.Weekday())
		}
		if b.Total() != 700 {
			t.Errorf("week total = %d, want 700", b.Total())
		}
	}
	cum := Buckets(days, "week", true)
	if cum[0].Total() != 700 || cum[1].Total() != 1400 {
		t.Errorf("cumulative = %d, %d; want 700, 1400", cum[0].Total(), cum[1].Total())
	}
}

// TestFoldSumsAndOrders checks that folding preserves the totals and ranks by
// the chosen unit.
func TestFoldSumsAndOrders(t *testing.T) {
	groups := []Group{
		{Repo: "a", Agent: "claude-code", Model: "opus", Requests: 2, Tokens: Tokens{Input: 10, Output: 1}},
		{Repo: "a", Agent: "claude-code", Model: "fable", Requests: 3, Tokens: Tokens{Input: 5, Output: 100}},
		{Repo: "b", Agent: "codex-cli", Model: "gpt", Requests: 1, Tokens: Tokens{Input: 40, Output: 2}},
	}
	byRepo := Fold(groups, "total", DefaultPricing, func(g Group) string { return g.Repo })
	if len(byRepo) != 2 {
		t.Fatalf("got %d repos, want 2", len(byRepo))
	}
	sum := 0
	for _, g := range byRepo {
		sum += g.Total()
	}
	if sum != 158 {
		t.Errorf("folded total = %d, want 158", sum)
	}
	if byRepo[0].Repo != "a" {
		t.Errorf("first repo = %q, want a (116 > 42)", byRepo[0].Repo)
	}
	byOutput := Fold(groups, "output", DefaultPricing, func(g Group) string { return g.Repo })
	if byOutput[0].Repo != "a" {
		t.Errorf("by output, first repo = %q, want a", byOutput[0].Repo)
	}
}

// TestViewsSumAlike checks the spec's invariant at the report layer: the repo,
// provider and model folds of one group set carry identical totals.
func TestViewsSumAlike(t *testing.T) {
	groups := []Group{
		{Repo: "a", Agent: "claude-code", Vendor: "anthropic", Model: "opus", Tokens: Tokens{Input: 10, CacheRead: 90, Output: 5}},
		{Repo: "", Agent: "codex-cli", Vendor: "openai", Model: "gpt", Tokens: Tokens{Input: 40, CacheCreate: 3, Output: 2}},
		{Repo: "a", Agent: "codex-cli", Vendor: "openai", Model: "gpt", Tokens: Tokens{Input: 1, Output: 1}},
	}
	want := Tokens{Input: 51, CacheRead: 90, CacheCreate: 3, Output: 8}
	for _, tt := range []struct {
		name string
		key  func(Group) string
	}{
		{"repo", func(g Group) string { return g.Repo }},
		{"provider", func(g Group) string { return g.Agent + g.Vendor }},
		{"model", func(g Group) string { return g.Model }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got Tokens
			for _, g := range Fold(groups, "total", DefaultPricing, tt.key) {
				got.Add(g.Tokens)
			}
			if got != want {
				t.Errorf("folded totals = %+v, want %+v", got, want)
			}
		})
	}
}

// TestUnit checks each unit against the disjoint-count convention.
func TestUnit(t *testing.T) {
	tok := Tokens{Input: 10, CacheRead: 100, CacheCreate: 20, Output: 5}
	p := Pricing{Input: 1, Output: 4, CacheRead: 0.1, CacheCreate: 0.2}
	for _, tt := range []struct {
		unit string
		want float64
	}{
		{"total", 135},
		{"context", 130},
		{"output", 5},
		{"input", 10},
		{"cost", 10 + 10 + 4 + 20},
		{"nonsense", 135},
	} {
		if got := tok.Unit(tt.unit, p); got != tt.want {
			t.Errorf("Unit(%q) = %v, want %v", tt.unit, got, tt.want)
		}
	}
}

// TestSummarize checks the header numbers a view prints above every body.
func TestSummarize(t *testing.T) {
	w := window(time.Date(2026, 6, 1, 0, 0, 0, 0, la), 7, la)
	hours := []Hour{
		hourAt(time.Date(2026, 6, 2, 12, 0, 0, 0, la), 100),
		hourAt(time.Date(2026, 6, 3, 12, 0, 0, 0, la), 400),
	}
	groups := []Group{
		{Model: "opus", Agent: "claude-code", Tokens: Tokens{Output: 400}},
		{Model: "gpt", Agent: "codex-cli", Tokens: Tokens{Output: 100}},
	}
	spans := []SessionSpan{
		{SessionID: "s1", First: time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC), Last: time.Date(2026, 6, 3, 1, 0, 0, 0, time.UTC)},
		{SessionID: "s2", First: time.Date(2026, 6, 3, 1, 0, 0, 0, time.UTC), Last: time.Date(2026, 6, 3, 2, 0, 0, 0, time.UTC)},
	}
	s := Summarize(Days(hours, w), groups, spans, w, "total", DefaultPricing)
	if s.ActiveDays != 2 || s.TotalDays != 7 {
		t.Errorf("active = %d/%d, want 2/7", s.ActiveDays, s.TotalDays)
	}
	if s.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", s.Sessions)
	}
	if s.LongestSession != 24*time.Hour || s.LongestID != "s1" {
		t.Errorf("longest session = %v (%s), want 24h (s1)", s.LongestSession, s.LongestID)
	}
	if !s.PeakDay.Equal(time.Date(2026, 6, 3, 0, 0, 0, 0, la)) {
		t.Errorf("peak day = %s, want 2026-06-03", s.PeakDay)
	}
	if s.TopModel != "opus" || s.TopModelShare != 0.8 {
		t.Errorf("top model = %q %.2f, want opus 0.80", s.TopModel, s.TopModelShare)
	}
	if s.Tokens.Total() != 500 {
		t.Errorf("total = %d, want 500", s.Tokens.Total())
	}
}

// TestWindowEndingAllTime checks that -since 0 starts at the oldest hour the
// index holds rather than at the epoch.
func TestWindowEndingAllTime(t *testing.T) {
	now := time.Date(2026, 6, 10, 15, 0, 0, 0, la)
	hours := []Hour{hourAt(time.Date(2026, 6, 4, 12, 0, 0, 0, la), 10)}
	w := WindowEnding(now, 0, la, hours)
	if !w.Start.Equal(time.Date(2026, 6, 4, 0, 0, 0, 0, la)) {
		t.Errorf("start = %s, want 2026-06-04", w.Start)
	}
	if !w.End.Equal(time.Date(2026, 6, 11, 0, 0, 0, 0, la)) {
		t.Errorf("end = %s, want 2026-06-11 (today included)", w.End)
	}
	if got := WindowEnding(now, 12*7, la, nil); got.Days() != 84 {
		t.Errorf("12w window = %d days, want 84", got.Days())
	}
}
