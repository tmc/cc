package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// barWidth is how many columns the widest bar in a ranked view occupies.
const barWidth = 24

// bar renders v as a proportion of max using eighth-block characters, so a
// small difference between two rows is still visible.
func bar(v, max float64, width int) string {
	if max <= 0 || v <= 0 {
		return ""
	}
	eighths := int(v / max * float64(width) * 8)
	if eighths == 0 {
		eighths = 1 // A non-zero value is never drawn as nothing.
	}
	full, rest := eighths/8, eighths%8
	s := strings.Repeat("█", full)
	if rest > 0 {
		s += string([]rune("▏▎▍▌▋▊▉")[rest-1])
	}
	return s
}

func (r report) weekday() error {
	rows := ccusage.Weekdays(r.days, r.opts.unit, r.price())
	max := 0.0
	for _, row := range rows {
		if row.Mean > max {
			max = row.Mean
		}
	}
	fmt.Printf("\n%s\n", labelStyle.Render("        mean/day     median   active  share"))
	for _, row := range rows {
		fmt.Printf("%s %10s %10s   %3d/%-3d %4s  %s\n",
			agentStyle.Render(row.Weekday.String()[:3]),
			compact(row.Mean), compact(row.Median),
			row.Active, row.Count, fmt.Sprintf("%.0f%%", row.Share*100),
			numberStyle.Render(bar(row.Mean, max, barWidth)))
	}
	return nil
}

func (r report) heatmap() error {
	grid := ccusage.Heatmap(r.hours, r.opts.window.Loc)
	var vals []float64
	max := 0.0
	for wd := 0; wd < 7; wd++ {
		for h := 0; h < 24; h++ {
			v := grid[wd][h].Unit(r.opts.unit, r.price())
			if v > 0 {
				vals = append(vals, v)
			}
			if v > max {
				max = v
			}
		}
	}
	sort.Float64s(vals)
	// The hour ruler lines up with the grid: five columns of row label, then
	// one character per local hour.
	fmt.Printf("\n%s\n", labelStyle.Render("     00    06    12    18  "))
	for n := 0; n < 7; n++ {
		wd := (n + 1) % 7
		var b strings.Builder
		for h := 0; h < 24; h++ {
			b.WriteString(levelRune(grid[wd][h].Unit(r.opts.unit, r.price()), vals))
		}
		fmt.Printf("%s  %s\n", agentStyle.Render(time.Weekday(wd).String()[:3]), b.String())
	}
	fmt.Printf("%s\n", labelStyle.Render("     · none  ░ ▒ ▓ █ quartiles of active hours · local time"))
	return nil
}

// levelRune renders v against the sorted non-zero values of its view.
func levelRune(v float64, sorted []float64) string {
	return string([]rune("·░▒▓█")[quartileOf(v, sorted)])
}

// quartileOf mirrors ccusage's calendar quantiles for grids that are not days.
func quartileOf(v float64, sorted []float64) int {
	if v <= 0 || len(sorted) == 0 {
		return 0
	}
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

func (r report) calendar() error {
	cal := ccusage.Calendar(r.days, r.opts.unit, r.price())
	if len(cal) == 0 {
		fmt.Println("\nno usage in the window")
		return nil
	}
	// Columns are weeks; rows are weekdays starting at the configured day.
	first := cal[0].Date
	back := (int(first.Weekday()) - int(r.opts.weekStart) + 7) % 7
	origin := civil(first.AddDate(0, 0, -back))
	// Counted in calendar days rather than elapsed hours: a daylight-saving
	// day is 23 or 25 hours long, and an hour of drift moves a cell into the
	// wrong week, which shows up as a row that does not line up with the rest.
	colOf := func(d time.Time) int {
		return int(civil(d).Sub(origin)/(24*time.Hour)) / 7
	}
	cols := colOf(cal[len(cal)-1].Date) + 1
	grid := make([][]int, 7)
	for i := range grid {
		grid[i] = make([]int, cols)
		for j := range grid[i] {
			grid[i][j] = -1 // Outside the window.
		}
	}
	months := make([]string, cols)
	for _, d := range cal {
		row := (int(d.Date.Weekday()) - int(r.opts.weekStart) + 7) % 7
		c := colOf(d.Date)
		if c < 0 || c >= cols {
			continue
		}
		grid[row][c] = d.Level
		if d.Date.Day() <= 7 && (months[c] == "" || d.Date.Day() == 1) {
			months[c] = d.Date.Format("Jan")
		}
	}
	// The month label sits over the column holding that month's first day,
	// unless the previous label would run into it.
	head := []rune(strings.Repeat(" ", cols))
	last := -3
	for c := 0; c < cols; c++ {
		if months[c] == "" || c-last < 3 {
			continue
		}
		for i, ch := range months[c] {
			if c+i < cols {
				head[c+i] = ch
			}
		}
		last = c
	}
	fmt.Printf("\n     %s\n", labelStyle.Render(string(head)))
	for row := 0; row < 7; row++ {
		wd := time.Weekday((int(r.opts.weekStart) + row) % 7)
		var b strings.Builder
		for c := 0; c < cols; c++ {
			if grid[row][c] < 0 {
				b.WriteString(" ")
				continue
			}
			b.WriteString(string([]rune("·░▒▓█")[grid[row][c]]))
		}
		fmt.Printf("%s  %s\n", agentStyle.Render(wd.String()[:3]), b.String())
	}
	fmt.Printf("%s\n", labelStyle.Render("     · none  ░ ▒ ▓ █ quartiles of active days"))
	r.printAgentShare()
	return nil
}

// civil is the date of t with its clock and zone discarded, so that two dates
// can be subtracted as whole days.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// printAgentShare prints the one-line agent breakdown the calendar carries.
func (r report) printAgentShare() {
	folded := ccusage.Fold(r.groups, r.opts.unit, r.price(), func(g ccusage.Group) string { return g.Agent })
	total := 0.0
	for _, g := range folded {
		total += g.Unit(r.opts.unit, r.price())
	}
	var parts []string
	for i, g := range folded {
		if i == 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s %.0f%%", g.Agent, ccusage.Share(g.Unit(r.opts.unit, r.price()), total)*100))
	}
	if len(parts) > 0 {
		fmt.Printf("%s\n", labelStyle.Render("by agent: "+strings.Join(parts, "  ")))
	}
}

func (r report) reposView(ctx context.Context) error {
	rank, err := r.rank(ctx, "repo")
	if err != nil {
		return err
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-38s %-24s %8s %9s  %5s",
		"repo", "agents", "sessions", r.opts.unit, "share")))
	for _, g := range rank.Rows {
		fmt.Printf("%-38s %-24s %8s %9s  %4.0f%%\n",
			truncate(g.Label, 38), truncate(strings.Join(g.Agents, ", "), 24),
			compactInt(g.Sessions), compact(g.Value), g.Share*100)
	}
	if rank.Rest != nil {
		fmt.Printf("%s\n", labelStyle.Render(fmt.Sprintf("%-38s %-24s %8s %9s  %4.0f%% (-n)",
			fmt.Sprintf("… %d more", rank.Count-len(rank.Rows)),
			truncate(strings.Join(rank.Rest.Agents, ", "), 24),
			compactInt(rank.Rest.Sessions), compact(rank.Rest.Value), rank.Rest.Share*100)))
	}
	return r.printAttribution(ctx, rank.Count)
}

// rank folds the report's groups onto one dimension. Both this and the web
// call service.UsageRanking, so a ranked row is folded once.
func (r report) rank(ctx context.Context, dim string) (ccusage.Ranking, error) {
	return r.svc.UsageRanking(ctx, r.groups, r.opts.filter, dim, r.opts.unit, r.priceFor, r.opts.top)
}

// printAttribution prints which of the four attribution steps placed the rows.
func (r report) printAttribution(ctx context.Context, repos int) error {
	counts, err := r.svc.UsageRowCounts(ctx, r.opts.filter, "attribution")
	if err != nil {
		return err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	names := map[string]string{"git": "by git dir", "path": "by path", "prefix": "by prefix", "": "none"}
	var parts []string
	for _, k := range []string{"git", "path", "prefix", ""} {
		if n, ok := counts[k]; ok && total > 0 {
			parts = append(parts, fmt.Sprintf("%.0f%% %s", float64(n)/float64(total)*100, names[k]))
		}
	}
	fmt.Printf("%s\n", labelStyle.Render(fmt.Sprintf("%d repos · attribution: %s",
		repos, strings.Join(parts, ", "))))
	return nil
}

func (r report) providers(ctx context.Context) error {
	rank, err := r.svc.UsageRanking(ctx, r.groups, r.opts.filter, "agent", r.opts.unit, r.priceFor, 0)
	if err != nil {
		return err
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-16s %-11s %8s %9s %9s  %5s",
		"agent", "vendor", "sessions", "requests", r.opts.unit, "share")))
	for _, g := range rank.Rows {
		fmt.Printf("%-16s %-11s %8s %9s %9s  %4.0f%%\n",
			g.Label, truncate(orNone(strings.Join(g.Vendors, ",")), 11), compactInt(g.Sessions),
			compactInt(g.Requests), compact(g.Value), g.Share*100)
	}
	return nil
}

func (r report) models(ctx context.Context) error {
	rank, err := r.rank(ctx, "model")
	if err != nil {
		return err
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-26s %-18s %6s %8s %8s %10s %10s %5s",
		"model", "agents", "share", "input", "output", "cache read", "cache write", "est.")))
	rows := rank.Rows
	if rank.Rest != nil {
		rows = append(rows, *rank.Rest)
	}
	for _, g := range rows {
		est := 0.0
		if g.Requests > 0 {
			est = float64(g.Estimated) / float64(g.Requests) * 100
		}
		fmt.Printf("%-26s %-18s %5.1f%% %8s %8s %10s %10s %4.0f%%\n",
			truncate(orNone(g.Label), 26), truncate(strings.Join(g.Agents, ","), 18),
			g.Share*100, compact(float64(g.Input)), compact(float64(g.Output)),
			compact(float64(g.CacheRead)), compact(float64(g.CacheCreate)), est)
	}
	return nil
}

// stackRunes are the fills a stacked daily bar draws its parts with, in the
// order the legend lists them.
var stackRunes = []string{"█", "▓", "▒", "░", "▪"}

func (r report) daily() error {
	buckets := ccusage.Buckets(r.days, r.opts.bucket, r.opts.cumulative)
	if r.opts.by != "" {
		return r.dailyStacked(buckets)
	}
	max := 0.0
	for _, b := range buckets {
		if v := b.Unit(r.opts.unit, r.price()); v > max {
			max = v
		}
	}
	label := "day"
	if r.opts.bucket == "week" {
		label = "week of"
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-12s %9s", label, r.opts.unit)))
	// A year of days is too many lines; print the tail that fits a screen.
	start := 0
	if n := len(buckets); n > 60 {
		start = n - 60
		fmt.Printf("%s\n", labelStyle.Render(fmt.Sprintf("… %d earlier buckets not shown (-since to widen the view)", start)))
	}
	for _, b := range buckets[start:] {
		if b.Requests == 0 && !r.opts.cumulative {
			continue
		}
		fmt.Printf("%-12s %9s  %s\n", b.Start.Format("2006-01-02"),
			compact(b.Unit(r.opts.unit, r.price())),
			numberStyle.Render(bar(b.Unit(r.opts.unit, r.price()), max, barWidth)))
	}
	note := "tallest " + compact(max)
	if !r.opts.cumulative {
		note += " · -cumulative for the running total"
	}
	fmt.Printf("%s\n", labelStyle.Render(note))
	return nil
}

// dailyStacked draws each bar as segments, one per value of the -by dimension.
// The parts come from one filtered hour query per value, which keeps the SQL
// the same shape as every other view's.
func (r report) dailyStacked(buckets []ccusage.Bucket) error {
	names, parts, err := r.byParts()
	if err != nil {
		return err
	}
	max := 0.0
	for _, b := range buckets {
		if v := b.Unit(r.opts.unit, r.price()); v > max {
			max = v
		}
	}
	label := "day"
	if r.opts.bucket == "week" {
		label = "week of"
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-12s %9s", label, r.opts.unit)))
	start := 0
	if n := len(buckets); n > 60 {
		start = n - 60
	}
	for i, b := range buckets {
		if i < start || (b.Requests == 0 && !r.opts.cumulative) {
			continue
		}
		var seg strings.Builder
		for j, name := range names {
			w := len([]rune(bar(parts[name][i].Unit(r.opts.unit, r.price()), max, barWidth)))
			seg.WriteString(strings.Repeat(stackRunes[j%len(stackRunes)], w))
		}
		fmt.Printf("%-12s %9s  %s\n", b.Start.Format("2006-01-02"),
			compact(b.Unit(r.opts.unit, r.price())), numberStyle.Render(seg.String()))
	}
	var legend []string
	for j, name := range names {
		legend = append(legend, stackRunes[j%len(stackRunes)]+" "+orNone(name))
	}
	fmt.Printf("%s\n", labelStyle.Render("tallest "+compact(max)+" · "+strings.Join(legend, "  ")))
	return nil
}

// byParts returns the top values of the -by dimension and each one's buckets,
// aligned with the unsplit buckets. Segments never sum to the bar when some
// rows are unattributed; the bar height stays the true total.
func (r report) byParts() ([]string, map[string][]ccusage.Bucket, error) {
	var key func(ccusage.Group) string
	switch r.opts.by {
	case "agent":
		key = func(g ccusage.Group) string { return g.Agent }
	case "model":
		key = func(g ccusage.Group) string { return g.Model }
	case "repo":
		key = func(g ccusage.Group) string { return g.Repo }
	default:
		return nil, nil, fmt.Errorf("unknown -by %q; want agent, model, or repo", r.opts.by)
	}
	folded := ccusage.Fold(r.groups, r.opts.unit, r.price(), key)
	if len(folded) > len(stackRunes) {
		folded = folded[:len(stackRunes)]
	}
	names := make([]string, 0, len(folded))
	parts := make(map[string][]ccusage.Bucket, len(folded))
	for _, g := range folded {
		v := key(g)
		if v == "" {
			// The filter has no way to say "no repo" or "no model", so the
			// unattributed part is left out of the split rather than drawn
			// as everything.
			continue
		}
		names = append(names, v)
		sub := r.opts.filter
		switch r.opts.by {
		case "agent":
			sub.Agent = v
		case "model":
			sub.Model = v
		case "repo":
			sub.Repo = v
		}
		hours, err := r.svc.UsageByHour(context.Background(), sub)
		if err != nil {
			return nil, nil, err
		}
		parts[v] = ccusage.Buckets(ccusage.Days(hourRows(hours), r.opts.window),
			r.opts.bucket, r.opts.cumulative)
	}
	return names, parts, nil
}

// sessions ranks the window's sessions by utilization and prints each one's
// share of the window. The share is always against the whole window, so
// -session narrows the rows printed but never the denominator: the question
// it answers is what fraction of the week one session was.
func (r report) sessions(ctx context.Context) error {
	rows, err := r.svc.UsageBySession(ctx, r.opts.filter)
	if err != nil {
		return err
	}
	total := 0.0
	for _, row := range rows {
		total += sessionTokens(row).Unit(r.opts.unit, r.price())
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf("%-10s %-22s %-13s %-16s %8s %9s %5s",
		"session", "repo", "agent", "when", "requests", r.opts.unit, "share")))
	shown := 0
	for _, row := range rows {
		if r.opts.session != "" && !strings.HasPrefix(row.SessionID, r.opts.session) {
			continue
		}
		if r.opts.session == "" && r.opts.top > 0 && shown >= r.opts.top {
			fmt.Printf("%s\n", labelStyle.Render(fmt.Sprintf("… %d more (-n)", len(rows)-shown)))
			break
		}
		shown++
		v := sessionTokens(row).Unit(r.opts.unit, r.price())
		when := row.First.In(r.opts.window.Loc).Format("Jan 2 15:04")
		fmt.Printf("%-10s %-22s %-13s %-16s %8s %9s %4.1f%%\n",
			shortID(row.SessionID), truncate(r.repoLabel(row.Repo), 22),
			truncate(orNone(row.Agent), 13), when, compactInt(row.Requests),
			compact(v), ccusage.Share(v, total)*100)
	}
	if shown == 0 {
		msg := "no usage in this window"
		if r.opts.session != "" {
			msg = "no session in this window starts with " + r.opts.session
		}
		fmt.Printf("%s\n", labelStyle.Render(msg))
	}
	return nil
}

// sessionTokens converts a store row to the report layer's token quadruple.
func sessionTokens(r store.SessionUsageRow) ccusage.Tokens {
	return ccusage.Tokens{Input: r.Input, CacheRead: r.CacheRead, CacheCreate: r.CacheCreate, Output: r.Output}
}
