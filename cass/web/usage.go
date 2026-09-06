package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// usageViews are the views GET /api/usage?view= serves. The names and the
// query parameters match the cass usage subcommand's, so a panel and a
// terminal asking the same question ask it the same way.
var usageViews = []string{"weekday", "heatmap", "calendar", "repos", "providers", "models", "daily", "sessions"}

// usageQuery is a parsed /api/usage request.
type usageQuery struct {
	view       string   // the first of views, and what the response echoes
	views      []string // every view the response carries
	unit       string
	filter     store.UsageFilter
	window     ccusage.Window
	loc        *time.Location
	days       int
	top        int
	bucket     string
	cumulative bool
	by         string
	weekStart  time.Weekday
}

// parseUsageQuery reads the view parameters from a request. It reports an
// error only for a value it cannot interpret; anything absent takes the same
// default the command line takes.
func parseUsageQuery(r *http.Request) (usageQuery, error) {
	q := r.URL.Query()
	u := usageQuery{
		view: "daily", views: []string{"daily"}, unit: "total", top: 20, bucket: "day",
		by: q.Get("by"), weekStart: time.Monday, loc: time.Local,
	}
	if v := q.Get("view"); v != "" {
		// Several views may be asked for at once, comma separated, so a panel
		// showing all of them costs one report rather than one each.
		u.views = strings.Split(v, ",")
		for _, name := range u.views {
			if !slicesContains(usageViews, name) {
				return u, fmt.Errorf("unknown view %q", name)
			}
		}
		u.view = u.views[0]
	}
	if v := q.Get("unit"); v != "" {
		if !slicesContains(ccusage.Units, v) {
			return u, fmt.Errorf("unknown unit %q", v)
		}
		u.unit = v
	}
	if v := q.Get("tz"); v != "" {
		loc, err := time.LoadLocation(v)
		if err != nil {
			return u, fmt.Errorf("unknown time zone %q", v)
		}
		u.loc = loc
	}
	if v := q.Get("bucket"); v != "" {
		u.bucket = v
	}
	if strings.HasPrefix(strings.ToLower(q.Get("week_start")), "sun") {
		u.weekStart = time.Sunday
	}
	u.cumulative = q.Get("cumulative") == "1" || q.Get("cumulative") == "true"
	if v := q.Get("n"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return u, fmt.Errorf("bad n %q", v)
		}
		u.top = n
	}
	u.filter = store.UsageFilter{
		Agent:            q.Get("agent"),
		Repo:             q.Get("repo"),
		Model:            q.Get("model"),
		Session:          q.Get("session"),
		ExcludeSidechain: q.Get("no_sidechain") == "1",
	}

	// The window: a count of days back from the end of the last complete day,
	// or everything the index holds when since is zero.
	days := 12 * 7
	if slicesContains(u.views, "calendar") {
		days = 52 * 7
	}
	if v := q.Get("since"); v != "" {
		n, err := parseSinceParam(v)
		if err != nil {
			return u, err
		}
		days = n
	}
	end := time.Now()
	if v := q.Get("until"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, u.loc)
		if err != nil {
			return u, fmt.Errorf("bad until %q", v)
		}
		end = t.AddDate(0, 0, 1)
	}
	u.days = days
	u.window = ccusage.Window{Loc: u.loc} // fitted to the rows when unbounded
	if days > 0 {
		u.window = ccusage.WindowEnding(end, days, u.loc, nil)
		if slicesContains(u.views, "calendar") {
			back := (int(u.window.Start.Weekday()) - int(u.weekStart) + 7) % 7
			u.window.Start = u.window.Start.AddDate(0, 0, -back)
		}
		u.filter.After, u.filter.Before = u.window.Start.UTC(), u.window.End.UTC()
	}
	return u, nil
}

// parseSinceParam reads a window length as a count of days: "12w", "30d", a
// Go duration, or "0" for everything.
func parseSinceParam(v string) (int, error) {
	switch {
	case v == "0":
		return 0, nil
	case strings.HasSuffix(v, "w"):
		n, err := strconv.Atoi(strings.TrimSuffix(v, "w"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad since %q", v)
		}
		return n * 7, nil
	case strings.HasSuffix(v, "d"):
		n, err := strconv.Atoi(strings.TrimSuffix(v, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("bad since %q", v)
		}
		return n, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("bad since %q", v)
	}
	return int(d.Hours() / 24), nil
}

// usageResponse is what a view returns. Only the fields the view needs are
// populated, so a calendar request does not carry a heatmap.
type usageResponse struct {
	View      string                  `json:"view"`
	Unit      string                  `json:"unit"`
	Zone      string                  `json:"zone"`
	Summary   ccusage.Summary         `json:"summary"`
	Days      []ccusage.Day           `json:"days,omitempty"`
	Weekdays  []ccusage.WeekdayRow    `json:"weekdays,omitempty"`
	Heatmap   *[7][24]ccusage.Tokens  `json:"heatmap,omitempty"`
	Calendar  []ccusage.CalDay        `json:"calendar,omitempty"`
	Buckets   []ccusage.Bucket        `json:"buckets,omitempty"`
	Repos     *ccusage.Ranking        `json:"repos,omitempty"`
	Providers *ccusage.Ranking        `json:"providers,omitempty"`
	Models    *ccusage.Ranking        `json:"models,omitempty"`
	Sessions  []store.SessionUsageRow `json:"sessions,omitempty"`

	// Attribution is how many usage rows each attribution step placed,
	// keyed by step; it accompanies the repo view, whose "(no repo)" row it
	// explains.
	Attribution map[string]int `json:"attribution,omitempty"`
}

// handleUsageView answers GET /api/usage?view=, the same report the cass usage
// command prints. Without a view parameter the handler keeps its original
// daily-token shape, which the existing panel reads.
func (s *Server) handleUsageView(w http.ResponseWriter, r *http.Request) {
	q, err := parseUsageQuery(r)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	rep, err := s.svc.UsageReport(r.Context(), service.UsageReportOptions{
		Filter: q.filter,
		Window: q.window,
		Unit:   q.unit,
		Price:  ccusage.PricingForModel("", ccusage.PricingConfig{}),
	})
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	priceFor := func(model string) ccusage.Pricing {
		return ccusage.PricingForModel(model, ccusage.PricingConfig{})
	}
	price := priceFor("")
	out := usageResponse{View: q.view, Unit: q.unit, Zone: ccusage.ZoneName(rep.Window.Loc), Summary: rep.Summary}
	out.Summary.Zone = out.Zone // the summary carries the same resolved name
	for _, view := range q.views {
		switch view {
		case "weekday":
			out.Weekdays = ccusage.Weekdays(rep.Days, q.unit, price)
		case "heatmap":
			h := ccusage.Heatmap(rep.Hours, rep.Window.Loc)
			out.Heatmap = &h
		case "calendar":
			out.Calendar = ccusage.Calendar(rep.Days, q.unit, price)
		case "daily":
			out.Days = rep.Days
			out.Buckets = ccusage.Buckets(rep.Days, q.bucket, q.cumulative)
			if q.by != "" {
				if err := s.stackBuckets(r, q, rep, price, out.Buckets); err != nil {
					writeError(w, err, http.StatusBadRequest)
					return
				}
			}
		case "repos", "providers", "models":
			dim := map[string]string{"repos": "repo", "providers": "agent", "models": "model"}[view]
			rank, err := s.svc.UsageRanking(r.Context(), rep.Groups, q.filter, dim, q.unit, priceFor, q.top)
			if err != nil {
				writeError(w, err, http.StatusInternalServerError)
				return
			}
			switch view {
			case "repos":
				out.Repos = &rank
				if out.Attribution, err = s.svc.UsageAttribution(r.Context(), q.filter); err != nil {
					writeError(w, err, http.StatusInternalServerError)
					return
				}
			case "providers":
				out.Providers = &rank
			case "models":
				out.Models = &rank
			}
		case "sessions":
			rows, err := s.svc.UsageBySession(r.Context(), q.filter)
			if err != nil {
				writeError(w, err, http.StatusInternalServerError)
				return
			}
			if len(rows) > q.top {
				rows = rows[:q.top]
			}
			out.Sessions = rows
		}
	}
	writeJSON(w, out)
}

// slicesContains reports whether v is in list.
func slicesContains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// stackBuckets fills each bucket's Parts with the split by q.by, one query per
// value of the dimension. Segments never sum to the bar when some rows are
// unattributed: the empty value is left out rather than drawn as everything,
// so the bar height stays the true total.
func (s *Server) stackBuckets(r *http.Request, q usageQuery, rep *service.UsageReport, p ccusage.Pricing, buckets []ccusage.Bucket) error {
	var key func(ccusage.Group) string
	switch q.by {
	case "agent":
		key = func(g ccusage.Group) string { return g.Agent }
	case "model":
		key = func(g ccusage.Group) string { return g.Model }
	case "repo":
		key = func(g ccusage.Group) string { return g.Repo }
	default:
		return fmt.Errorf("unknown by %q; want agent, model, or repo", q.by)
	}
	folded := ccusage.Fold(rep.Groups, q.unit, p, key)
	if len(folded) > usageStackMax {
		folded = folded[:usageStackMax]
	}
	for _, g := range folded {
		v := key(g)
		if v == "" {
			continue
		}
		sub := q.filter
		switch q.by {
		case "agent":
			sub.Agent = v
		case "model":
			sub.Model = v
		case "repo":
			sub.Repo = v
		}
		hours, err := s.svc.UsageByHour(r.Context(), sub)
		if err != nil {
			return err
		}
		part := ccusage.Buckets(ccusage.Days(service.UsageHours(hours), q.window), q.bucket, q.cumulative)
		for i := range part {
			if i >= len(buckets) {
				break
			}
			if buckets[i].Parts == nil {
				buckets[i].Parts = make(map[string]ccusage.Tokens)
			}
			buckets[i].Parts[v] = part[i].Tokens
		}
	}
	return nil
}

// usageStackMax bounds the segments a stacked bar carries, as the terminal
// view is bounded by its rune palette.
const usageStackMax = 8
