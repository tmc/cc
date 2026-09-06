package ccusage

import (
	"math"
	"sort"
	"time"
)

// Tokens is the four-way split every report carries. The fields are disjoint,
// as in [Record].
type Tokens struct {
	Input       int `json:"input"`
	CacheRead   int `json:"cache_read"`
	CacheCreate int `json:"cache_create"`
	Output      int `json:"output"`
}

// Context returns the prompt tokens the model read.
func (t Tokens) Context() int { return t.Input + t.CacheRead + t.CacheCreate }

// Total returns the context plus the completion.
func (t Tokens) Total() int { return t.Context() + t.Output }

// Add accumulates o into t.
func (t *Tokens) Add(o Tokens) {
	t.Input += o.Input
	t.CacheRead += o.CacheRead
	t.CacheCreate += o.CacheCreate
	t.Output += o.Output
}

// Unit returns t measured in unit: "total", "context", "output", "input", or
// "cost", which weights the four counts by p. An unknown unit is total.
func (t Tokens) Unit(unit string, p Pricing) float64 {
	switch unit {
	case "context":
		return float64(t.Context())
	case "output":
		return float64(t.Output)
	case "input":
		return float64(t.Input)
	case "cost":
		return float64(t.Input)*p.Input + float64(t.CacheRead)*p.CacheRead +
			float64(t.CacheCreate)*p.CacheCreate + float64(t.Output)*p.Output
	default:
		return float64(t.Total())
	}
}

// Units are the unit names [Tokens.Unit] accepts.
var Units = []string{"total", "context", "output", "input", "cost"}

// Hour is the usage of one UTC hour: cass/store's HourRow, restated here so
// that the report functions depend on no storage package.
type Hour struct {
	Hour      int64 // unix hour, ts/3600
	Requests  int
	Estimated int
	Tokens
}

// Group is the usage of one (repo, agent, vendor, model) combination.
type Group struct {
	Repo      string    `json:"repo"`
	Agent     string    `json:"agent"`
	Vendor    string    `json:"vendor"`
	Model     string    `json:"model"`
	Sessions  int       `json:"sessions"`
	Requests  int       `json:"requests"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
	Estimated int       `json:"estimated"`
	Tokens
}

// SessionSpan is one session's extent within a window.
type SessionSpan struct {
	SessionID   string
	First, Last time.Time
	Requests    int
}

// Window is the local time range a report covers, half-open: [Start, End).
// Both ends are midnight in Loc.
type Window struct {
	Start, End time.Time      `json:"-"`
	Loc        *time.Location `json:"-"`
}

// Days returns the number of whole local days in the window.
func (w Window) Days() int {
	if !w.End.After(w.Start) {
		return 0
	}
	n := 0
	for d := w.Start; d.Before(w.End); d = nextDay(d) {
		n++
	}
	return n
}

// nextDay returns the local midnight after d, which is not d plus 24 hours on
// a daylight-saving transition.
func nextDay(d time.Time) time.Time {
	n := d.AddDate(0, 0, 1)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, d.Location())
}

// WindowEnding returns the window of n whole local days ending at the last
// complete day before now. n of zero means all time, and the window then runs
// from the first hour present in hours.
func WindowEnding(now time.Time, days int, loc *time.Location, hours []Hour) Window {
	now = now.In(loc)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if days > 0 {
		return Window{Start: end.AddDate(0, 0, -days), End: end, Loc: loc}
	}
	start := end
	for _, h := range hours {
		t := time.Unix(h.Hour*3600, 0).In(loc)
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
		if d.Before(start) {
			start = d
		}
	}
	return Window{Start: start, End: nextDay(end), Loc: loc}
}

// Day is one local calendar day of usage.
type Day struct {
	Date      time.Time `json:"date"` // local midnight
	Requests  int       `json:"requests"`
	Estimated int       `json:"estimated"`
	Tokens
}

// Days folds UTC hours into local calendar days covering the whole window,
// including days with no usage, ordered oldest first. Hours outside the window
// are dropped.
func Days(hours []Hour, w Window) []Day {
	byDay := make(map[time.Time]*Day)
	var out []Day
	for d := w.Start; d.Before(w.End); d = nextDay(d) {
		out = append(out, Day{Date: d})
	}
	for i := range out {
		byDay[out[i].Date] = &out[i]
	}
	for _, h := range hours {
		t := time.Unix(h.Hour*3600, 0).In(w.Loc)
		d, ok := byDay[time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, w.Loc)]
		if !ok {
			continue
		}
		d.Requests += h.Requests
		d.Estimated += h.Estimated
		d.Add(h.Tokens)
	}
	return out
}

// WeekdayRow is one weekday's average over a window.
type WeekdayRow struct {
	Weekday time.Weekday `json:"weekday"`
	Mean    float64      `json:"mean"` // unit per calendar day of this weekday, zero days included
	Median  float64      `json:"median"`
	Active  int          `json:"active"` // days with any usage
	Count   int          `json:"count"`  // calendar days of this weekday in the window
	Share   float64      `json:"share"`
	Tokens
}

// Weekdays averages days by weekday, measuring each day in unit. The divisor
// is the number of calendar occurrences of that weekday in the window, so an
// idle Sunday pulls its mean down rather than vanishing.
func Weekdays(days []Day, unit string, p Pricing) []WeekdayRow {
	var rows [7]WeekdayRow
	var vals [7][]float64
	total := 0.0
	for _, d := range days {
		i := int(d.Date.Weekday())
		v := d.Unit(unit, p)
		rows[i].Count++
		rows[i].Add(d.Tokens)
		vals[i] = append(vals[i], v)
		if d.Requests > 0 {
			rows[i].Active++
		}
		total += v
	}
	out := make([]WeekdayRow, 0, 7)
	// Monday first, as the calendar and ISO weeks do.
	for n := 0; n < 7; n++ {
		i := (n + 1) % 7
		r := rows[i]
		r.Weekday = time.Weekday(i)
		sum := 0.0
		for _, v := range vals[i] {
			sum += v
		}
		if r.Count > 0 {
			r.Mean = sum / float64(r.Count)
		}
		r.Median = median(vals[i])
		if total > 0 {
			r.Share = sum / total
		}
		out = append(out, r)
	}
	return out
}

// median returns the median of vals, which it sorts a copy of. An empty slice
// has median zero.
func median(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// Heatmap folds hours into a weekday-by-local-hour grid, indexed
// [time.Weekday][hour].
func Heatmap(hours []Hour, loc *time.Location) [7][24]Tokens {
	var grid [7][24]Tokens
	for _, h := range hours {
		t := time.Unix(h.Hour*3600, 0).In(loc)
		grid[int(t.Weekday())][t.Hour()].Add(h.Tokens)
	}
	return grid
}

// CalDay is one cell of the calendar: a local day and its intensity level,
// 0 for no usage and 1 to 4 for the quartiles of the window's active days.
type CalDay struct {
	Day
	Level int `json:"level"`
}

// Calendar returns every day in the window with an intensity level, measuring
// each day in unit. Levels are quartiles of the active days rather than
// fractions of the maximum, so one enormous day does not flatten the year.
func Calendar(days []Day, unit string, p Pricing) []CalDay {
	var active []float64
	for _, d := range days {
		if v := d.Unit(unit, p); v > 0 {
			active = append(active, v)
		}
	}
	sort.Float64s(active)
	out := make([]CalDay, len(days))
	for i, d := range days {
		out[i] = CalDay{Day: d, Level: quartile(d.Unit(unit, p), active)}
	}
	return out
}

// quartile returns 0 for a non-positive v, else 1 to 4 by which quarter of the
// sorted active values v falls in.
func quartile(v float64, sorted []float64) int {
	if v <= 0 || len(sorted) == 0 {
		return 0
	}
	// The count of active values at or below v decides the quarter.
	n := sort.SearchFloat64s(sorted, v)
	for n < len(sorted) && sorted[n] <= v {
		n++
	}
	q := (n*4 + len(sorted) - 1) / len(sorted)
	if q < 1 {
		q = 1
	}
	if q > 4 {
		q = 4
	}
	return q
}

// Bucket is one bar of the daily view: a day or the Monday of a week.
type Bucket struct {
	Start    time.Time `json:"start"`
	Requests int       `json:"requests"`
	Tokens
	// Parts is the split by the dimension the view stacks on, empty when it
	// stacks on nothing.
	Parts map[string]Tokens `json:"parts,omitempty"`
}

// Buckets groups days by "day" or "week" (ISO, Monday-based) and, when
// cumulative, returns running totals instead of per-bucket sums.
func Buckets(days []Day, size string, cumulative bool) []Bucket {
	var out []Bucket
	key := func(d time.Time) time.Time {
		if size != "week" {
			return d
		}
		back := (int(d.Weekday()) + 6) % 7 // Monday is 0.
		return d.AddDate(0, 0, -back)
	}
	for _, d := range days {
		k := key(d.Date)
		if len(out) == 0 || !out[len(out)-1].Start.Equal(k) {
			out = append(out, Bucket{Start: k})
		}
		b := &out[len(out)-1]
		b.Requests += d.Requests
		b.Add(d.Tokens)
	}
	if cumulative {
		var run Tokens
		n := 0
		for i := range out {
			run.Add(out[i].Tokens)
			n += out[i].Requests
			out[i].Tokens, out[i].Requests = run, n
		}
	}
	return out
}

// Summary is the header every view prints.
type Summary struct {
	Window   Window `json:"-"`
	Zone     string `json:"zone"`
	Unit     string `json:"unit"`
	Tokens   Tokens `json:"tokens"`
	Requests int    `json:"requests"`
	// Estimated is the share of requests whose output was estimated, 0 to 1.
	Estimated      float64       `json:"estimated"`
	Sessions       int           `json:"sessions"`
	ActiveDays     int           `json:"active_days"`
	TotalDays      int           `json:"total_days"`
	PeakDay        time.Time     `json:"peak_day"`
	PeakValue      float64       `json:"peak_value"`
	CurrentStreak  int           `json:"current_streak"`
	LongestStreak  int           `json:"longest_streak"`
	LongestSession time.Duration `json:"longest_session"`
	LongestID      string        `json:"longest_session_id"`
	TopModel       string        `json:"top_model"`
	TopModelShare  float64       `json:"top_model_share"`
}

// Summarize builds the header from the same rows the body is drawn from.
func Summarize(days []Day, groups []Group, spans []SessionSpan, w Window, unit string, p Pricing) Summary {
	s := Summary{Window: w, Zone: w.Loc.String(), Unit: unit, TotalDays: len(days)}
	est := 0
	for _, d := range days {
		s.Tokens.Add(d.Tokens)
		s.Requests += d.Requests
		est += d.Estimated
		v := d.Unit(unit, p)
		if d.Requests > 0 {
			s.ActiveDays++
		}
		if v > s.PeakValue {
			s.PeakDay, s.PeakValue = d.Date, v
		}
	}
	if s.Requests > 0 {
		s.Estimated = float64(est) / float64(s.Requests)
	}
	s.CurrentStreak, s.LongestStreak = streaks(days)

	byModel := make(map[string]float64)
	totalModel := 0.0
	for _, g := range groups {
		if g.Model == "" {
			continue
		}
		v := g.Unit(unit, p)
		byModel[g.Model] += v
		totalModel += v
	}
	for _, m := range sortedKeys(byModel) {
		if v := byModel[m]; v > byModel[s.TopModel] || s.TopModel == "" {
			s.TopModel = m
		}
	}
	if totalModel > 0 && s.TopModel != "" {
		s.TopModelShare = byModel[s.TopModel] / totalModel
	}

	sessions := make(map[string]bool)
	for _, sp := range spans {
		sessions[sp.SessionID] = true
		if d := sp.Last.Sub(sp.First); d > s.LongestSession {
			s.LongestSession, s.LongestID = d, sp.SessionID
		}
	}
	s.Sessions = len(sessions)
	return s
}

// sortedKeys returns m's keys in order, so a tie between two equal values is
// broken the same way on every run.
func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// streaks returns the current and longest runs of consecutive active days.
// The current streak counts back from the last day in the window, which is
// the last complete local day.
func streaks(days []Day) (current, longest int) {
	run := 0
	for _, d := range days {
		if d.Requests > 0 {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	for i := len(days) - 1; i >= 0 && days[i].Requests > 0; i-- {
		current++
	}
	return current, longest
}

// Fold collapses groups onto the dimensions key returns, summing the rest.
// The result is ordered by unit, largest first. Sessions is the sum of the
// folded rows' counts, which double-counts a session that used more than one
// model; callers that print it replace it with an exact count.
func Fold(groups []Group, unit string, p Pricing, key func(Group) string) []Group {
	byKey := make(map[string]*Group)
	var order []string
	for _, g := range groups {
		k := key(g)
		cur, ok := byKey[k]
		if !ok {
			n := g
			n.Sessions = 0
			byKey[k], order = &n, append(order, k)
			cur = &n
			cur.Tokens = Tokens{}
			cur.Requests = 0
			cur.Estimated = 0
		}
		cur.Add(g.Tokens)
		cur.Requests += g.Requests
		cur.Sessions += g.Sessions
		cur.Estimated += g.Estimated
		if cur.First.IsZero() || (!g.First.IsZero() && g.First.Before(cur.First)) {
			cur.First = g.First
		}
		if g.Last.After(cur.Last) {
			cur.Last = g.Last
		}
	}
	out := make([]Group, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Unit(unit, p) > out[j].Unit(unit, p)
	})
	return out
}

// Share returns v as a fraction of total, or zero when total is zero.
func Share(v, total float64) float64 {
	if total == 0 || math.IsNaN(total) {
		return 0
	}
	return v / total
}

// RankRow is one row of a ranked view: every group sharing one value of the
// ranked dimension, folded together and measured in the view's unit. Name is
// the raw value, empty for the unattributed bucket; Label is what a reader
// sees, which for a repository is its short name rather than its git dir.
type RankRow struct {
	Name      string    `json:"name"`
	Label     string    `json:"label"`
	Agents    []string  `json:"agents,omitempty"`
	Vendors   []string  `json:"vendors,omitempty"`
	Sessions  int       `json:"sessions"`
	Requests  int       `json:"requests"`
	Estimated int       `json:"estimated"`
	First     time.Time `json:"first"`
	Last      time.Time `json:"last"`
	Tokens
	Value float64 `json:"value"` // Tokens measured in the ranking's unit
	Share float64 `json:"share"` // Value over the ranking's total
}

// Ranking is what a ranked view shows: the rows above the cut, an aggregate of
// those below it, and the total they all sum to. Rest exists so that the rows
// a reader is given always account for the whole window: a client that adds up
// what it was sent gets the same number the summary reports.
type Ranking struct {
	Unit  string    `json:"unit"`
	Rows  []RankRow `json:"rows"`
	Rest  *RankRow  `json:"rest,omitempty"`
	Total float64   `json:"total"`
	Count int       `json:"count"` // rows before the cut
}

// Rank folds groups onto one dimension and orders them by unit, descending.
// Sessions is left at zero: the fact table cannot count distinct sessions per
// folded value without a second query, so the caller fills it in from
// UsageSessionCounts, and Label from whatever names the value.
func Rank(groups []Group, unit string, price func(model string) Pricing, key func(Group) string) []RankRow {
	folded := Fold(groups, unit, price(""), key)
	// Value is summed over the unfolded groups, so that a model's own rates
	// apply even when the fold mixes models -- a repository's cost is the sum
	// of what each model it ran cost, not its total priced at one rate.
	value := make(map[string]float64)
	for _, g := range groups {
		value[key(g)] += g.Unit(unit, price(g.Model))
	}
	agents := make(map[string]map[string]bool)
	vendors := make(map[string]map[string]bool)
	for _, g := range groups {
		k := key(g)
		if agents[k] == nil {
			agents[k], vendors[k] = make(map[string]bool), make(map[string]bool)
		}
		if g.Agent != "" {
			agents[k][g.Agent] = true
		}
		if g.Vendor != "" {
			vendors[k][g.Vendor] = true
		}
	}
	total := 0.0
	for _, g := range folded {
		total += value[key(g)]
	}
	rows := make([]RankRow, len(folded))
	for i, g := range folded {
		k := key(g)
		v := value[k]
		rows[i] = RankRow{
			Name: k, Label: k, Agents: sortedSet(agents[k]), Vendors: sortedSet(vendors[k]),
			Requests: g.Requests, Estimated: g.Estimated,
			First: g.First, Last: g.Last, Tokens: g.Tokens,
			Value: v, Share: Share(v, total),
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Value > rows[j].Value })
	return rows
}

// Cut keeps the first n rows and folds the rest into one row, so that a
// truncated view still sums to the total. The unattributed row is kept
// wherever it ranks: it measures the attribution gap rather than being one
// value among many. A non-positive n keeps everything.
func Cut(rows []RankRow, unit string, n int) Ranking {
	total := 0.0
	for _, r := range rows {
		total += r.Value
	}
	out := Ranking{Unit: unit, Total: total, Count: len(rows)}
	if n <= 0 || len(rows) <= n {
		out.Rows = rows
		return out
	}
	out.Rows = make([]RankRow, 0, n+1)
	out.Rows = append(out.Rows, rows[:n]...)
	rest := RankRow{Name: "", Label: "(rest)"}
	agents := make(map[string]bool)
	for _, r := range rows[n:] {
		if r.Name == "" {
			out.Rows = append(out.Rows, r)
			continue
		}
		rest.Tokens.Add(r.Tokens)
		rest.Requests += r.Requests
		rest.Sessions += r.Sessions
		rest.Estimated += r.Estimated
		rest.Value += r.Value
		for _, a := range r.Agents {
			agents[a] = true
		}
	}
	if rest.Requests > 0 || rest.Value > 0 {
		rest.Agents = sortedSet(agents)
		rest.Share = Share(rest.Value, total)
		out.Rest = &rest
	}
	return out
}

// sortedSet returns the set's members in order.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ZoneName returns the name to print for loc. A location loaded from the
// process environment calls itself "Local", which says nothing to a reader, so
// it is resolved to the zone's own abbreviation for the current time.
func ZoneName(loc *time.Location) string {
	if loc == nil {
		return ""
	}
	if n := loc.String(); n != "Local" {
		return n
	}
	return time.Now().In(loc).Format("MST")
}
