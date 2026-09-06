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

// checkTolerance is the fraction by which the index may differ from Claude's
// own ledger before a row is called out.
const checkTolerance = 0.02

// runUsageCheck compares the index's Claude Code rows against
// ~/.claude/stats-cache.json, per model and per day. Output is reported but
// not asserted on: the JSONL snapshot problem is ours to estimate and theirs
// to have, so the two are expected to differ there.
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
			if c.field != "output†" && absf(d) > checkTolerance {
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
	days := make([]string, 0, len(sc.DailyModelTokens))
	want := make(map[string]int)
	for _, d := range sc.DailyModelTokens {
		sum := 0
		for _, n := range d.TokensByModel {
			sum += n
		}
		want[d.Date], days = sum, append(days, d.Date)
	}
	sort.Strings(days)
	off := 0
	for _, d := range days {
		if absf(delta(byDay[d], want[d])) > checkTolerance {
			off++
		}
	}
	fmt.Printf("\n%s\n", labelStyle.Render(fmt.Sprintf(
		"daily totals: %d of %d days within %.0f%% (%d off)",
		len(days)-off, len(days), checkTolerance*100, off)))
	fmt.Printf("%s\n", labelStyle.Render(
		"† output is reported, not asserted: the JSONL snapshot is estimated on both sides"))
	if bad > 0 {
		return fmt.Errorf("%d model fields outside %.0f%%", bad, checkTolerance*100)
	}
	return nil
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

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
