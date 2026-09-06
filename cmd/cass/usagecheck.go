package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// statsCache is the part of ~/.claude/stats-cache.json the cross-check reads.
// Claude Code computes it from the same session files cass indexes, so it is
// an independent count of the same events rather than a second source.
type statsCache struct {
	LastComputedDate string `json:"lastComputedDate"`
	ModelUsage       map[string]struct {
		InputTokens              int `json:"inputTokens"`
		OutputTokens             int `json:"outputTokens"`
		CacheReadInputTokens     int `json:"cacheReadInputTokens"`
		CacheCreationInputTokens int `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
	DailyModelTokens []struct {
		Date          string         `json:"date"`
		TokensByModel map[string]int `json:"tokensByModel"`
	} `json:"dailyModelTokens"`
}

// checkTolerance is the margin the sign rule allows before it fails: the index
// may not read above Claude's own ledger by more than this.
const checkTolerance = 0.02

// runUsageCheck compares the index's Claude Code rows against
// ~/.claude/stats-cache.json, per model and per day.
//
// Only one thing is asserted: the index must never read above the ledger. The
// two are not otherwise comparable, for two reasons the numbers make plain.
// The ledger is a cumulative accumulator whose transcripts are deleted after
// cleanupPeriodDays, so a lifetime comparison counts sessions that are no
// longer on disk for cass to read; the per-day comparison is therefore
// restricted to days no older than the oldest transcript. And within those
// days the ledger tracks the sum taken one streamed entry at a time, not one
// per request: on this corpus the unfolded sum matches it to the token on the
// days a single session accounts for the whole day, while the folded sum — the
// tokens actually billed — sits at about half. Both ratios are printed so the
// reading is visible rather than inferred.
func runUsageCheck(ctx context.Context, svc *service.Service, unit string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "stats-cache.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read stats cache: %w", err)
	}
	var sc statsCache
	if err := json.Unmarshal(data, &sc); err != nil {
		return fmt.Errorf("parse stats cache: %w", err)
	}

	f := store.UsageFilter{Agent: "claude-code"}
	groups, err := svc.UsageByGroup(ctx, f)
	if err != nil {
		return err
	}
	byModel := ccusage.Fold(groupRows(groups), "total", ccusage.DefaultPricing,
		func(g ccusage.Group) string { return g.Model })

	fmt.Printf("%s\n", headerStyle.Render("cass usage -check stats-cache"))
	fmt.Printf("%s\n\n", labelStyle.Render(
		"comparing agent claude-code against "+path+", computed "+sc.LastComputedDate))

	fmt.Printf("%-30s %12s %12s %7s  %s\n", labelStyle.Render("model"),
		labelStyle.Render("cass"), labelStyle.Render("stats-cache"),
		labelStyle.Render("delta"), labelStyle.Render("field"))
	bad := 0
	for _, g := range byModel {
		want, ok := sc.ModelUsage[g.Model]
		if !ok {
			continue
		}
		for _, c := range []struct {
			field    string
			got, exp int
		}{
			{"input", g.Input, want.InputTokens},
			{"cache read", g.CacheRead, want.CacheReadInputTokens},
			{"cache write", g.CacheCreate, want.CacheCreationInputTokens},
			{"output†", g.Output, want.OutputTokens},
		} {
			d := delta(c.got, c.exp)
			mark := ""
			if c.field != "output†" && d > checkTolerance {
				mark, bad = " ✗", bad+1
			}
			fmt.Printf("%-30s %12s %12s %6.1f%%  %s%s\n", truncate(g.Model, 30),
				compact(float64(c.got)), compact(float64(c.exp)), d*100, c.field, mark)
		}
	}

	// Per-day totals, over the days the cache covers.
	hours, err := svc.UsageByHour(ctx, f)
	if err != nil {
		return err
	}
	loc := time.Local
	byDay := make(map[string]int)
	for _, h := range hourRows(hours) {
		t := time.Unix(h.Hour*3600, 0).In(loc)
		byDay[t.Format("2006-01-02")] += h.Total()
	}
	unfolded, oldest, err := transcriptSums(ctx, loc)
	if err != nil {
		return err
	}
	first := oldest.In(loc).Format("2006-01-02")

	days := make([]string, 0, len(sc.DailyModelTokens))
	want := make(map[string]int)
	skipped := 0
	for _, d := range sc.DailyModelTokens {
		sum := 0
		for _, n := range d.TokensByModel {
			sum += n
		}
		if d.Date < first {
			// The transcripts those tokens were counted from are gone.
			skipped++
			continue
		}
		want[d.Date], days = sum, append(days, d.Date)
	}
	sort.Strings(days)

	fmt.Printf("\n%-12s %12s %12s %7s %12s %7s\n",
		labelStyle.Render("day"), labelStyle.Render("cass"),
		labelStyle.Render("stats-cache"), labelStyle.Render("cass/sc"),
		labelStyle.Render("per-entry"), labelStyle.Render("pe/sc"))
	var foldRatios, entryRatios []float64
	over := 0
	for _, d := range days {
		if want[d] == 0 {
			continue
		}
		raw := 0
		for _, n := range unfolded[d] {
			raw += n
		}
		fr, er := float64(byDay[d])/float64(want[d]), float64(raw)/float64(want[d])
		foldRatios, entryRatios = append(foldRatios, fr), append(entryRatios, er)
		mark := ""
		if fr > 1+checkTolerance {
			mark, over = " ✗", over+1
		}
		fmt.Printf("%-12s %12s %12s %6.2fx %12s %6.2fx%s\n", d,
			compact(float64(byDay[d])), compact(float64(want[d])), fr,
			compact(float64(raw)), er, mark)
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf(
		"%d days compared, from %s, the oldest transcript on disk; %d earlier days in the "+
			"ledger have no transcripts left to read", len(days), first, skipped)))
	fmt.Printf("%s\n", labelStyle.Render(fmt.Sprintf(
		"median cass/stats-cache %.2fx · median per-entry/stats-cache %.2fx",
		median(foldRatios), median(entryRatios))))
	fmt.Printf("%s\n", labelStyle.Render(
		"the ledger sums one streamed entry at a time; cass sums one request at a time, "+
			"so cass reading below it is expected and only reading above it is a defect"))
	fmt.Printf("%s\n", labelStyle.Render(
		"† output is reported, not asserted: the JSONL snapshot is estimated on both sides"))
	if bad > 0 || over > 0 {
		return fmt.Errorf("%d model fields and %d days read above the ledger", bad, over)
	}
	return nil
}

// median returns the middle value of vs, or zero when vs is empty.
func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	s := append([]float64(nil), vs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// delta returns (got-exp)/exp, or zero when both are zero.
func delta(got, exp int) float64 {
	if exp == 0 {
		if got == 0 {
			return 0
		}
		return 1
	}
	return float64(got-exp) / float64(exp)
}
