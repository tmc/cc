package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// usageViews are the views `cass usage` renders, in the order its help lists
// them.
var usageViews = []string{"weekday", "heatmap", "calendar", "repos", "providers", "models", "daily", "sessions"}

// usageOpts is the parsed command line, shared by every view so that the
// summary header and the body are drawn from the same window.
type usageOpts struct {
	view       string
	window     ccusage.Window
	unit       string
	pricing    ccusage.PricingConfig
	filter     store.UsageFilter
	top        int
	bucket     string
	cumulative bool
	by         string
	session    string
	jsonOut    bool
	weekStart  time.Weekday
}

func runUsage(ctx context.Context, svc *service.Service, args []string, jsonOut bool) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	since := fs.String("since", "12w", "window length (Nw, Nd, or 0 for all time)")
	until := fs.String("until", "", "end of the window (YYYY-MM-DD, default the last complete day)")
	unit := fs.String("unit", "total", "unit of utilization: "+strings.Join(ccusage.Units, "|"))
	agent := fs.String("agent", "", "restrict to one harness (claude-code, codex-cli, ...)")
	repo := fs.String("repo", "", "restrict to one repository, by git common dir or name")
	model := fs.String("model", "", "restrict to one model")
	tz := fs.String("tz", "", "IANA zone for day boundaries (default local)")
	noSide := fs.Bool("no-sidechain", false, "exclude subagent requests")
	n := fs.Int("n", 20, "rows to print in a ranked view")
	bucket := fs.String("bucket", "day", "daily view bucket: day or week")
	cumulative := fs.Bool("cumulative", false, "daily view shows a running total")
	by := fs.String("by", "", "daily view split: agent, model, or repo")
	weekStart := fs.String("week-start", "mon", "first weekday of a calendar column: mon or sun")
	format := fs.String("format", "", "output format: text or json")
	session := fs.String("session", "", "sessions view: show only this session id (prefix match)")
	check := fs.String("check", "", "cross-check against a native ledger: stats-cache")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: cass usage <view> [flags]\n\nviews:\n  %s\n\nflags:\n",
			strings.Join(usageViews, "\n  "))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	view := "weekday"
	if len(rest) > 0 {
		view = rest[0]
		// Flags may follow the view name.
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
	}
	if *check != "" {
		if *check != "stats-cache" {
			return fmt.Errorf("unknown -check %q", *check)
		}
		return runUsageCheck(ctx, svc, *unit)
	}
	if !contains(usageViews, view) {
		return fmt.Errorf("unknown view %q; want one of %s", view, strings.Join(usageViews, ", "))
	}
	if !contains(ccusage.Units, *unit) {
		return fmt.Errorf("unknown -unit %q; want one of %s", *unit, strings.Join(ccusage.Units, ", "))
	}

	loc, err := zone(*tz)
	if err != nil {
		return err
	}
	days, err := parseSince(*since)
	if err != nil {
		return err
	}
	end := time.Now()
	if *until != "" {
		t, err := time.ParseInLocation("2006-01-02", *until, loc)
		if err != nil {
			return fmt.Errorf("parse -until: %w", err)
		}
		end = t.AddDate(0, 0, 1)
	}
	if view == "calendar" && *since == "12w" {
		days = 52 * 7 // The calendar's own default is a year.
	}

	opts := usageOpts{
		view: view, unit: *unit, top: *n, bucket: *bucket,
		cumulative: *cumulative, by: *by, session: *session,
		jsonOut:   jsonOut || *format == "json",
		weekStart: time.Monday,
	}
	if strings.HasPrefix(strings.ToLower(*weekStart), "sun") {
		opts.weekStart = time.Sunday
	}
	opts.filter = store.UsageFilter{Agent: *agent, Model: *model, ExcludeSidechain: *noSide}

	repos, err := svc.Repos(ctx)
	if err != nil {
		return err
	}
	if *repo != "" {
		dir, ok := findRepo(repos, *repo)
		if !ok {
			return fmt.Errorf("no indexed repository matches %q", *repo)
		}
		opts.filter.Repo = dir
	}

	// The window is needed before the query when it is bounded, and after it
	// when -since 0 asks for everything the index holds.
	if days > 0 {
		opts.window = ccusage.WindowEnding(end, days, loc, nil)
		opts.filter.After, opts.filter.Before = opts.window.Start.UTC(), opts.window.End.UTC()
	}
	hours, err := svc.UsageByHour(ctx, opts.filter)
	if err != nil {
		return err
	}
	if days <= 0 {
		opts.window = ccusage.WindowEnding(end, 0, loc, hourRows(hours))
	}

	groups, err := svc.UsageByGroup(ctx, opts.filter)
	if err != nil {
		return err
	}
	spans, err := svc.UsageSessions(ctx, opts.filter)
	if err != nil {
		return err
	}

	rep := report{
		opts:   opts,
		repos:  repos,
		hours:  hourRows(hours),
		groups: groupRows(groups),
		spans:  spanRows(spans),
		svc:    svc,
	}
	rep.days = ccusage.Days(rep.hours, opts.window)
	rep.summary = ccusage.Summarize(rep.days, rep.groups, rep.spans, opts.window, opts.unit, rep.price())
	rep.summary.Zone = zoneName(opts.window.Loc)

	if err := warnBackfill(ctx, svc, opts.window); err != nil {
		return err
	}
	return rep.render(ctx)
}

// report holds everything a view draws from.
type report struct {
	opts    usageOpts
	svc     *service.Service
	repos   map[string]store.Repo
	hours   []ccusage.Hour
	groups  []ccusage.Group
	spans   []ccusage.SessionSpan
	days    []ccusage.Day
	summary ccusage.Summary
}

// price returns the rates the cost unit weights by. Rows that still carry a
// model are weighted at that model's rate; day and weekday rows have folded
// several models together already, so they use the default rate and the
// header says so.
func (r report) price() ccusage.Pricing {
	return ccusage.PricingForModel("", r.opts.pricing)
}

// priceFor returns the rates for one model.
func (r report) priceFor(model string) ccusage.Pricing {
	return ccusage.PricingForModel(model, r.opts.pricing)
}

func (r report) render(ctx context.Context) error {
	if r.opts.jsonOut {
		return r.renderJSON()
	}
	fmt.Print(r.header())
	switch r.opts.view {
	case "weekday":
		return r.weekday()
	case "heatmap":
		return r.heatmap()
	case "calendar":
		return r.calendar()
	case "repos":
		return r.reposView(ctx)
	case "providers":
		return r.providers(ctx)
	case "models":
		return r.models(ctx)
	case "daily":
		return r.daily()
	case "sessions":
		return r.sessions(ctx)
	}
	return fmt.Errorf("unknown view %q", r.opts.view)
}

// header is the summary block every view prints first.
func (r report) header() string {
	s := r.summary
	var b strings.Builder
	unit := s.Unit
	if unit == "cost" {
		unit = "cost (relative, default rates over folded rows)"
	}
	fmt.Fprintf(&b, "%s · %s → %s · %s · unit: %s\n",
		headerStyle.Render(viewTitle(r.opts.view)),
		s.Window.Start.Format("2006-01-02"),
		s.Window.End.AddDate(0, 0, -1).Format("2006-01-02"),
		zoneName(s.Window.Loc), unit)
	fmt.Fprintf(&b, "total %s · sessions %s · requests %s · active %d/%d",
		numberStyle.Render(compact(s.Tokens.Unit(s.Unit, r.price()))),
		compactInt(s.Sessions), compactInt(s.Requests), s.ActiveDays, s.TotalDays)
	if !s.PeakDay.IsZero() {
		fmt.Fprintf(&b, " · peak %s (%s)", s.PeakDay.Format("Jan 2"), compact(s.PeakValue))
	}
	fmt.Fprintf(&b, "\nstreak %dd (longest %dd)", s.CurrentStreak, s.LongestStreak)
	if s.LongestSession > 0 {
		fmt.Fprintf(&b, " · longest session %s (%s)", shortDur(s.LongestSession), shortID(s.LongestID))
	}
	if s.TopModel != "" {
		fmt.Fprintf(&b, " · top model %s %.0f%%", s.TopModel, s.TopModelShare*100)
	}
	b.WriteString("\n")
	t := s.Tokens
	// The absolute counts as well as the shares, so the line can be read
	// against what /stats and /usage print for the same window.
	split := func(name string, n int) string {
		return fmt.Sprintf("%s %s (%s)", name, compact(float64(n)), pct(n, t.Total()))
	}
	fmt.Fprintf(&b, "%s\n", labelStyle.Render(fmt.Sprintf("%s · %s · %s · %s · estimated output %.0f%% of requests",
		split("input", t.Input), split("cache read", t.CacheRead),
		split("cache write", t.CacheCreate), split("output", t.Output), s.Estimated*100)))
	return b.String()
}

func viewTitle(view string) string {
	switch view {
	case "weekday":
		return "Token utilization by weekday"
	case "heatmap":
		return "Token utilization by weekday and hour"
	case "calendar":
		return "Token activity"
	case "repos":
		return "Token utilization by repository"
	case "providers":
		return "Token utilization by provider"
	case "models":
		return "Token utilization by model"
	case "sessions":
		return "Token utilization by session"
	default:
		return "Token activity over time"
	}
}
