package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccusage"
)

// collectRecords normalizes every file's usage into requests, naming each
// file's harness so that each one's token convention is undone once. It is the
// same normalization the index applies, over a list of files instead.
func collectRecords(files []string) ([]ccusage.Record, error) {
	var recs []ccusage.Record
	for _, path := range files {
		entries, err := cc.ReadFile(context.Background(), path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		recs = append(recs, ccusage.FromEntries(agentForFile(path, entries), entries)...)
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("no API requests found")
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Time.Before(recs[j].Time) })
	return recs, nil
}

// runReport draws the weekday or repos view over a list of files, from the
// same report functions the index-backed cass usage command uses.
func runReport(mode string, files []string) error {
	pricingCfg, err := loadPricingConfig(*pricingFileFlag)
	if err != nil {
		return err
	}
	recs, err := collectRecords(files)
	if err != nil {
		return err
	}
	price := func(model string) ccusage.Pricing { return ccusage.PricingForModel(model, pricingCfg) }
	unit := reportUnit()
	hours, groups, spans := ccusage.Aggregate(recs)
	window := ccusage.WindowEnding(time.Now(), 0, time.Local, hours)
	days := ccusage.Days(hours, window)
	summary := ccusage.Summarize(days, groups, spans, window, unit, price(""))

	if mode == "weekday" {
		rows := ccusage.Weekdays(days, unit, price(""))
		if *formatFlag == "json" {
			return writeReportJSON(mode, unit, window, summary, rows)
		}
		printReportHeader(mode, unit, window, summary)
		fmt.Printf("%-10s %12s %12s %8s %6s\n", "weekday", "mean", "median", "active", "share")
		for _, r := range rows {
			fmt.Printf("%-10s %12.0f %12.0f %8d %5.0f%%\n",
				r.Weekday.String(), r.Mean, r.Median, r.Active, r.Share*100)
		}
		return nil
	}

	rank := ccusage.Cut(ccusage.Rank(groups, unit, price, func(g ccusage.Group) string { return g.Repo }), unit, *topNFlag)
	counts := sessionsByDir(recs)
	for i := range rank.Rows {
		rank.Rows[i].Label = dirLabel(rank.Rows[i].Name)
		rank.Rows[i].Sessions = counts[rank.Rows[i].Name]
	}
	if rank.Rest != nil {
		// The rest row counts the sessions of the directories it covers, so a
		// session working in two of them is counted once, as it is in a row.
		kept := make(map[string]bool)
		for _, r := range rank.Rows {
			kept[r.Name] = true
		}
		rest := make(map[string]bool)
		for _, rec := range recs {
			if !kept[rec.CWD] {
				rest[rec.SessionID] = true
			}
		}
		rank.Rest.Sessions = len(rest)
	}
	if *formatFlag == "json" {
		return writeReportJSON(mode, unit, window, summary, rank)
	}
	printReportHeader(mode, unit, window, summary)
	fmt.Printf("%-38s %8s %12s %6s\n", "directory", "sessions", unit, "share")
	rows := rank.Rows
	if rank.Rest != nil {
		rows = append(rows, *rank.Rest)
	}
	for _, r := range rows {
		fmt.Printf("%-38s %8d %12.0f %5.0f%%\n", truncate(r.Label, 38), r.Sessions, r.Value, r.Share*100)
	}
	return nil
}

// reportUnit maps the characteristics unit flag onto a report unit. The two
// share a flag, and the report functions do not weight by request count.
func reportUnit() string {
	if *unitFlag == "requests" || *unitFlag == "tokens" {
		return "total"
	}
	return *unitFlag
}

// sessionsByDir counts the distinct sessions that worked in each directory.
// A session that moved between directories counts in each, as it does in the
// index, where the same session can carry rows for more than one repository.
func sessionsByDir(recs []ccusage.Record) map[string]int {
	seen := make(map[string]map[string]bool)
	for _, r := range recs {
		if seen[r.CWD] == nil {
			seen[r.CWD] = make(map[string]bool)
		}
		seen[r.CWD][r.SessionID] = true
	}
	counts := make(map[string]int, len(seen))
	for dir, ids := range seen {
		counts[dir] = len(ids)
	}
	return counts
}

// dirLabel names a working directory for a reader. Files carry no git
// resolution, so the directory itself is the repository.
func dirLabel(dir string) string {
	if dir == "" {
		return "(no directory)"
	}
	return filepath.Base(dir)
}

// truncate shortens s to n runes, ending in an ellipsis.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func printReportHeader(mode, unit string, w ccusage.Window, s ccusage.Summary) {
	fmt.Printf("ccstats %s · %s to %s · %s · %d sessions · %d requests\n\n",
		mode, w.Start.Format("2006-01-02"), w.End.Format("2006-01-02"),
		unit, s.Sessions, s.Requests)
}

func writeReportJSON(mode, unit string, w ccusage.Window, s ccusage.Summary, body any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		View    string          `json:"view"`
		Unit    string          `json:"unit"`
		Zone    string          `json:"zone"`
		Start   string          `json:"start"`
		End     string          `json:"end"`
		Summary ccusage.Summary `json:"summary"`
		Body    any             `json:"body"`
	}{mode, unit, ccusage.ZoneName(w.Loc),
		w.Start.Format("2006-01-02"), w.End.Format("2006-01-02"), s, body})
}
