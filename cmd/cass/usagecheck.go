package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// Only one thing is asserted: the index must never read above the ledger. For
// that assertion to mean anything the two must count the same population, so
// the comparison buckets days in UTC, as the ledger does, and leaves subagent
// rows out, as the ledger does. Neither is cosmetic: comparing local days read
// nine of sixty-two days high, and counting subagents read four of them 1.4x
// to 1.6x high.
//
// The two are not otherwise comparable, for two reasons the numbers make plain.
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

	// The ledger counts only what the main thread spent: a subagent's tokens
	// are absent from it, so comparing against rows that carry them would be
	// comparing two different populations. Measured on the days a subagent
	// dominates, the main-thread entry sum lands at 0.98-0.99 of the ledger
	// where the sum with subagents lands at 2.5-3.2x it.
	f := store.UsageFilter{Agent: "claude-code", ExcludeSidechain: true}
	groups, err := svc.UsageByGroup(ctx, f)
	if err != nil {
		return err
	}
	byModel := ccusage.Fold(groupRows(groups), "total", ccusage.DefaultPricing,
		func(g ccusage.Group) string { return g.Model })

	fmt.Printf("%s\n", headerStyle.Render("cass usage -check stats-cache"))
	fmt.Printf("%s\n\n", labelStyle.Render(
		"comparing agent claude-code against "+path+", computed "+sc.LastComputedDate))

	fmt.Printf("%-14s %12s %14s %10s\n", labelStyle.Render("field"),
		labelStyle.Render("cass"), labelStyle.Render("stats-cache"),
		labelStyle.Render("delta"))
	bad := 0
	for _, g := range byModel {
		want, ok := sc.ModelUsage[g.Model]
		if !ok {
			continue
		}
		fmt.Printf("\n%s\n", modelStyle.Render(g.Model))
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
			fmt.Printf("  %-12s %12s %14s %9.1f%%%s\n", c.field,
				compact(float64(c.got)), compact(float64(c.exp)), d*100, mark)
		}
	}

	// Per-day totals, over the days the cache covers.
	hours, err := svc.UsageByHour(ctx, f)
	if err != nil {
		return err
	}
	// The ledger buckets a day in UTC, so the comparison does too. Bucketing
	// locally shifts tokens across midnight and reads some days high and
	// others low against the same ledger.
	loc := time.UTC
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

	fmt.Printf("\n%-12s %12s %14s %9s %14s %9s   %s\n",
		labelStyle.Render("day"), labelStyle.Render("cass"),
		labelStyle.Render("stats-cache"), labelStyle.Render("cass/sc"),
		labelStyle.Render("per-entry"), labelStyle.Render("pe/sc"),
		labelStyle.Render("cass/sc, 1.0 = the ledger"))
	var foldRatios, entryRatios []float64
	over, month := 0, ""
	for _, d := range days {
		if want[d] == 0 {
			continue
		}
		if m := d[:7]; m != month {
			// A blank line at each month, so sixty days of rows can be read
			// a stretch at a time rather than as one block.
			fmt.Println()
			month = m
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
		fmt.Printf("%-12s %12s %14s %8.2fx %14s %8.2fx   %s%s\n", d,
			compact(float64(byDay[d])), compact(float64(want[d])), fr,
			compact(float64(raw)), er, ratioBar(fr), mark)
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

// checkBarWidth is where the ledger's own figure falls on the ratio bar, so a
// day cass reads above it is the one bar that crosses the mark.
const checkBarWidth = 16

// ratioBar draws a ratio against a fixed 1.0 mark rather than against the
// largest row, since the whole reading is whether a day sits below the ledger
// or above it.
func ratioBar(r float64) string {
	if r <= 0 {
		return labelStyle.Render(strings.Repeat("·", 1))
	}
	s := bar(r, 1, checkBarWidth)
	if r > 1+checkTolerance {
		return overStyle.Render(s)
	}
	return s
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
