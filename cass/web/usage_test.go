package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/ccusage"
)

// usageTestServer indexes one session whose requests span three days, so every
// view has something to fold.
func usageTestServer(t *testing.T) http.Handler {
	t.Helper()
	day := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	var events []cass.UsageEvent
	for i := range 6 {
		events = append(events, cass.UsageEvent{
			Record: ccusage.Record{
				SessionID: "usage-view-1",
				RequestID: fmt.Sprintf("req_%d", i),
				Agent:     "claude-code",
				Time:      day.AddDate(0, 0, i/2).Add(time.Duration(i) * time.Hour),
				Model:     "claude-opus-5",
				Vendor:    "anthropic",
				Input:     10, CacheRead: 20, CacheCreate: 5, Output: 7,
			},
			GitCommonDir: "/work/repo/.git",
			Attribution:  "git",
		})
	}
	svc, err := service.New(service.Config{
		DBPath: filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{testCollector{sessions: []cass.Session{{
			ID:         "usage-view-1",
			Agent:      "claude-code",
			Title:      "Usage views",
			Workspace:  "/work/repo",
			SourcePath: "/tmp/usage-view.jsonl",
			StartedAt:  day,
			EndedAt:    day.AddDate(0, 0, 3),
			Usage:      events,
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	if _, err := svc.Index(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return New(Config{Service: svc}).Handler()
}

func TestUsageViews(t *testing.T) {
	handler := usageTestServer(t)
	const query = "&since=52w&until=2026-03-10&tz=UTC"
	for _, view := range usageViews {
		t.Run(view, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/usage?view="+view+query, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
			}
			var got usageResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.View != view {
				t.Fatalf("view = %q, want %q", got.View, view)
			}
			// Every view is drawn from the same window, so the summary is the
			// same whichever one asked for it.
			if want := 6 * (10 + 20 + 5 + 7); got.Summary.Tokens.Total() != want {
				t.Fatalf("total = %d, want %d", got.Summary.Tokens.Total(), want)
			}
			if got.Summary.Requests != 6 {
				t.Fatalf("requests = %d, want 6", got.Summary.Requests)
			}
		})
	}
}

func TestUsageViewBodies(t *testing.T) {
	handler := usageTestServer(t)
	get := func(q string) usageResponse {
		t.Helper()
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage?"+q, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body %q", q, rr.Code, rr.Body.String())
		}
		var got usageResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: decode: %v", q, err)
		}
		return got
	}
	const query = "&since=4w&until=2026-03-10&tz=UTC"

	if got := get("view=weekday" + query); len(got.Weekdays) != 7 {
		t.Fatalf("weekdays = %d, want 7", len(got.Weekdays))
	}
	if got := get("view=heatmap" + query); got.Heatmap == nil {
		t.Fatal("heatmap missing")
	} else if got.Heatmap[time.Monday][12].Total() == 0 {
		t.Fatalf("heatmap has no Monday noon usage: %v", got.Heatmap[time.Monday][12])
	}
	// The zone is the resolved name, not the placeholder "Local", and the
	// summary carries the same one the response does.
	if got := get("view=weekday&since=4w&until=2026-03-10&tz=America/New_York"); got.Zone != "America/New_York" ||
		got.Summary.Zone != got.Zone {
		t.Fatalf("zone = %q, summary zone = %q", got.Zone, got.Summary.Zone)
	}
	if got := get("view=calendar" + query); len(got.Calendar) == 0 {
		t.Fatal("calendar empty")
	}
	// One repository row, which is the unattributed bucket when the indexer
	// could not resolve the working directory: it is shown, never dropped.
	if got := get("view=repos" + query); got.Repos == nil || len(got.Repos.Rows) != 1 ||
		got.Repos.Rows[0].Label != "(no repo)" || got.Repos.Rows[0].Total() != 252 {
		t.Fatalf("repos = %#v", got.Repos)
	} else if got.Attribution == nil {
		t.Fatal("repo view carries no attribution breakdown")
	}
	if got := get("view=providers" + query); got.Providers == nil || len(got.Providers.Rows) != 1 ||
		got.Providers.Rows[0].Name != "claude-code" {
		t.Fatalf("providers = %#v", got.Providers)
	}
	if got := get("view=sessions" + query); len(got.Sessions) != 1 {
		t.Fatalf("sessions = %#v", got.Sessions)
	}

	// The stacked daily view splits each bar without changing its height.
	daily := get("view=daily&by=model" + query)
	if len(daily.Buckets) == 0 {
		t.Fatal("no buckets")
	}
	var stacked int
	for _, b := range daily.Buckets {
		if p, ok := b.Parts["claude-opus-5"]; ok {
			stacked++
			if p.Total() != b.Total() {
				t.Fatalf("part %d != bar %d", p.Total(), b.Total())
			}
		}
	}
	if stacked == 0 {
		t.Fatal("no bucket carries a model part")
	}
}

func TestUsageViewRejectsBadParameters(t *testing.T) {
	handler := usageTestServer(t)
	for _, q := range []string{"view=nope", "view=daily&unit=nope", "view=daily&tz=Mars/Olympus", "view=daily&n=0", "view=daily&since=x", "view=daily&by=nope"} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage?"+q, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rr.Code)
		}
	}
}

// TestUsageLegacyShape guards the panel that predates the views: a request
// without one keeps its original body.
func TestUsageLegacyShape(t *testing.T) {
	handler := usageTestServer(t)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage?since=720h", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"daily", "latest_limits", "api_request_count"} {
		if _, ok := body[k]; !ok {
			t.Errorf("legacy body missing %q", k)
		}
	}
}

// TestUsageMultiView asks for every view at once: the panel draws them all
// from one report rather than one request each.
func TestUsageMultiView(t *testing.T) {
	handler := usageTestServer(t)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/api/usage?view=daily,weekday,heatmap,calendar,repos,models,providers&since=4w&until=2026-03-10&tz=UTC", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var got usageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Buckets) == 0 || len(got.Weekdays) != 7 || got.Heatmap == nil ||
		len(got.Calendar) == 0 || got.Repos == nil || got.Models == nil || got.Providers == nil ||
		len(got.Repos.Rows) != 1 || len(got.Models.Rows) != 1 || len(got.Providers.Rows) != 1 {
		t.Fatalf("multi-view response missing a view: %+v", got)
	}
	// Every view is folded from the same rows, so they agree on the total.
	if got.Repos.Rows[0].Total() != got.Models.Rows[0].Total() || got.Repos.Rows[0].Total() != got.Summary.Tokens.Total() {
		t.Fatalf("views disagree: repos %d models %d summary %d",
			got.Repos.Rows[0].Total(), got.Models.Rows[0].Total(), got.Summary.Tokens.Total())
	}
}

// TestUsageAllTimeFitsWindow checks that since=0 fits the window to the rows
// the index holds instead of returning an empty report.
func TestUsageAllTimeFitsWindow(t *testing.T) {
	handler := usageTestServer(t)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/usage?view=daily&since=0&tz=UTC", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rr.Code, rr.Body.String())
	}
	var got usageResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := 6 * (10 + 20 + 5 + 7); got.Summary.Tokens.Total() != want {
		t.Fatalf("total = %d, want %d", got.Summary.Tokens.Total(), want)
	}
	if got.Summary.TotalDays == 0 || len(got.Buckets) == 0 {
		t.Fatalf("window not fitted: %+v", got.Summary)
	}
}
