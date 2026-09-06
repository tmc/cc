package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// hourRows converts store rows to the report package's own types, which is
// what keeps ccusage free of a dependency on the store.
func hourRows(rows []store.HourRow) []ccusage.Hour {
	out := make([]ccusage.Hour, len(rows))
	for i, r := range rows {
		out[i] = ccusage.Hour{
			Hour: r.Hour, Requests: r.Requests, Estimated: r.Estimated,
			Tokens: ccusage.Tokens{Input: r.Input, CacheRead: r.CacheRead,
				CacheCreate: r.CacheCreate, Output: r.Output},
		}
	}
	return out
}

func groupRows(rows []store.GroupRow) []ccusage.Group {
	out := make([]ccusage.Group, len(rows))
	for i, r := range rows {
		out[i] = ccusage.Group{
			Repo: r.Repo, Agent: r.Agent, Vendor: r.Vendor, Model: r.Model,
			Sessions: r.Sessions, Requests: r.Requests, First: r.First, Last: r.Last,
			Estimated: r.Estimated,
			Tokens: ccusage.Tokens{Input: r.Input, CacheRead: r.CacheRead,
				CacheCreate: r.CacheCreate, Output: r.Output},
		}
	}
	return out
}

func spanRows(rows []store.SessionSpanRow) []ccusage.SessionSpan {
	out := make([]ccusage.SessionSpan, len(rows))
	for i, r := range rows {
		out[i] = ccusage.SessionSpan{SessionID: r.SessionID, First: r.First, Last: r.Last, Requests: r.Requests}
	}
	return out
}

// zoneName is how a report names its display zone. time.Local reports itself
// as "Local", which says nothing, so the current abbreviation stands in.
func zoneName(loc *time.Location) string {
	if n := loc.String(); n != "Local" {
		return n
	}
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	return time.Now().In(loc).Format("MST")
}

// zone resolves -tz, falling back to the process zone.
func zone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load zone %q: %w", name, err)
	}
	return loc, nil
}

// parseSince reads a window length as a number of days. "12w" is 84, "30d" is
// 30, and "0" means all time.
func parseSince(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(strings.TrimRight(s, "wdWD"))
	if err != nil {
		return 0, fmt.Errorf("parse -since %q: want Nw, Nd, or 0", s)
	}
	switch unit {
	case 'w', 'W':
		return n * 7, nil
	case 'd', 'D':
		return n, nil
	default:
		return 0, fmt.Errorf("parse -since %q: want Nw, Nd, or 0", s)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// findRepo resolves a -repo argument against the indexed repositories: an
// exact git common dir, else a repository whose name, remote, or path contains
// the argument.
func findRepo(repos map[string]store.Repo, want string) (string, bool) {
	if _, ok := repos[want]; ok {
		return want, true
	}
	var hits []string
	for dir, r := range repos {
		if strings.Contains(r.Name, want) || strings.Contains(r.Remote, want) || strings.Contains(dir, want) {
			hits = append(hits, dir)
		}
	}
	sort.Strings(hits)
	if len(hits) == 0 {
		return "", false
	}
	return hits[0], true
}

// repoLabel is how the repo view names a git common dir.
func (r report) repoLabel(dir string) string {
	if dir == "" {
		return "(no repo)"
	}
	if repo, ok := r.repos[dir]; ok {
		if repo.Name != "" {
			return repo.Name
		}
		if repo.Remote != "" {
			return repo.Remote
		}
	}
	return filepath.Base(filepath.Dir(dir))
}

// warnBackfill prints a line when the index predates usage_events, since cass
// has no backfill: sessions indexed before the table existed hold no rows and
// silently shrink every total.
func warnBackfill(ctx context.Context, svc *service.Service, w ccusage.Window) error {
	n, oldest, err := svc.UsageCoverage(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintln(os.Stderr, "cass: no usage events indexed; run `cass index --force`")
		return nil
	}
	first, err := svc.OldestSession(ctx)
	if err != nil {
		return err
	}
	if !first.IsZero() && oldest.Sub(first) > 24*time.Hour {
		fmt.Fprintf(os.Stderr, "cass: usage rows start %s but sessions start %s; "+
			"run `cass index --force` to backfill the gap\n",
			oldest.Format("2006-01-02"), first.Format("2006-01-02"))
	}
	return nil
}

// compact renders a token count the way both harnesses' own views do.
func compact(v float64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1e9:
		return fmt.Sprintf("%.2fB", v/1e9)
	case abs >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case abs >= 1e3:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
}

// compactInt renders a count with thin spaces between thousands.
func compactInt(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 4 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(" ")
		}
		b.WriteRune(c)
	}
	return b.String()
}

// pct renders n as a percentage of total with one decimal.
func pct(n, total int) string {
	if total == 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(n)/float64(total)*100)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// shortDur renders a duration the way the harnesses' headers do.
func shortDur(d time.Duration) string {
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// usageJSON is what -format json emits: the same rows the text views draw,
// plus the header, so a client need not redo any of the arithmetic.
type usageJSON struct {
	View     string               `json:"view"`
	Unit     string               `json:"unit"`
	Zone     string               `json:"zone"`
	Start    string               `json:"start"`
	End      string               `json:"end"`
	Summary  ccusage.Summary      `json:"summary"`
	Days     []ccusage.CalDay     `json:"days,omitempty"`
	Weekdays []ccusage.WeekdayRow `json:"weekdays,omitempty"`
	Buckets  []ccusage.Bucket     `json:"buckets,omitempty"`
	Groups   []ccusage.Group      `json:"groups,omitempty"`
	Heatmap  [][]ccusage.Tokens   `json:"heatmap,omitempty"`
}

func (r report) renderJSON() error {
	out := usageJSON{
		View: r.opts.view, Unit: r.opts.unit, Zone: zoneName(r.opts.window.Loc),
		Start:   r.opts.window.Start.Format("2006-01-02"),
		End:     r.opts.window.End.Format("2006-01-02"),
		Summary: r.summary,
	}
	switch r.opts.view {
	case "weekday":
		out.Weekdays = ccusage.Weekdays(r.days, r.opts.unit, r.price())
	case "calendar":
		out.Days = ccusage.Calendar(r.days, r.opts.unit, r.price())
	case "daily":
		out.Buckets = ccusage.Buckets(r.days, r.opts.bucket, r.opts.cumulative)
	case "heatmap":
		grid := ccusage.Heatmap(r.hours, r.opts.window.Loc)
		for wd := 0; wd < 7; wd++ {
			out.Heatmap = append(out.Heatmap, grid[wd][:])
		}
	default:
		out.Groups = r.groups
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
