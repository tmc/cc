// Command ccstats reports token usage, tool counts, and timing for sessions.
//
// Usage:
//
//	ccstats [flags] [file...]
//	ccstats session.jsonl
//	ccstats -since 24h
//
// Examples:
//
//	ccstats ~/.claude/projects/*/44fc759a*.jsonl
//	ccstats -since 16h -format json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	sinceFlag  = flag.String("since", "", "Find sessions modified within duration (e.g. 16h, 7d)")
	formatFlag = flag.String("format", "text", "Output format: text, json")
)

// Stats holds aggregated statistics for a session.
type Stats struct {
	SessionID string `json:"session_id,omitempty"`
	File      string `json:"file"`
	Slug      string `json:"slug,omitempty"`
	Model     string `json:"model,omitempty"`

	// Timing.
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
	Duration time.Duration `json:"duration"`

	// Counts.
	UserMessages int `json:"user_messages"`
	AsstMessages int `json:"asst_messages"`
	Compactions  int `json:"compactions"`
	TotalEntries int `json:"total_entries"`

	// Tokens.
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	CacheReadTokens   int `json:"cache_read_tokens"`
	CacheCreateTokens int `json:"cache_create_tokens"`

	// Tools.
	ToolUses  map[string]int `json:"tool_uses"`
	TotalTool int            `json:"total_tool_uses"`
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccstats: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	files, err := resolveFiles()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no files specified; use file args or -since flag")
	}

	var allStats []Stats
	for _, f := range files {
		s, err := statsForFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f, err)
			continue
		}
		if s.TotalEntries == 0 {
			continue
		}
		allStats = append(allStats, s)
	}

	sort.Slice(allStats, func(i, j int) bool {
		return allStats[i].End.After(allStats[j].End)
	})

	return output(allStats)
}

func resolveFiles() ([]string, error) {
	args := flag.Args()

	// If files given as args, use them.
	if len(args) > 0 {
		return args, nil
	}

	// If stdin is piped, read from stdin (expect file paths).
	fi, _ := os.Stdin.Stat()
	if fi.Mode()&os.ModeCharDevice == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				files = append(files, line)
			}
		}
		return files, nil
	}

	// Use -since to find files.
	if *sinceFlag == "" {
		return nil, nil
	}
	since, err := parseDuration(*sinceFlag)
	if err != nil {
		return nil, err
	}
	return cc.FindSessionFiles(since, "")
}

func statsForFile(path string) (Stats, error) {
	entries, err := cc.ReadFile(path)
	if err != nil {
		return Stats{}, err
	}

	s := Stats{
		File:     path,
		ToolUses: make(map[string]int),
	}

	for _, e := range entries {
		s.TotalEntries++

		if e.SessionID != "" && s.SessionID == "" {
			s.SessionID = e.SessionID
		}
		if e.Slug != "" && s.Slug == "" {
			s.Slug = e.Slug
		}

		if !e.Timestamp.IsZero() {
			if s.Start.IsZero() {
				s.Start = e.Timestamp
			}
			s.End = e.Timestamp
		}

		if e.Type == "system" && e.Subtype == "compact_boundary" {
			s.Compactions++
		}

		if e.Message == nil {
			continue
		}

		// Skip compact summaries from message counts — they're synthetic.
		if e.IsCompactSummary {
			continue
		}

		switch e.Message.Role {
		case "user":
			s.UserMessages++
		case "assistant":
			s.AsstMessages++
			if s.Model == "" && e.Message.Model != "" {
				s.Model = e.Message.Model
			}
			for _, tu := range e.Message.ToolUses() {
				s.ToolUses[tu.Name]++
				s.TotalTool++
			}
		}

		if e.Message.Usage != nil {
			u := e.Message.Usage
			s.InputTokens += u.InputTokens
			s.OutputTokens += u.OutputTokens
			s.CacheReadTokens += u.CacheReadInputTokens
			s.CacheCreateTokens += u.CacheCreationInputTokens
		}
	}

	if !s.Start.IsZero() && !s.End.IsZero() {
		s.Duration = s.End.Sub(s.Start)
	}

	return s, nil
}

func output(stats []Stats) error {
	if *formatFlag == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stats)
	}

	// Aggregate totals.
	var totalIn, totalOut, totalCacheRead, totalCacheCreate int
	var totalUser, totalAsst, totalToolUse int
	toolTotals := make(map[string]int)

	for _, s := range stats {
		totalIn += s.InputTokens
		totalOut += s.OutputTokens
		totalCacheRead += s.CacheReadTokens
		totalCacheCreate += s.CacheCreateTokens
		totalUser += s.UserMessages
		totalAsst += s.AsstMessages
		totalToolUse += s.TotalTool
		for k, v := range s.ToolUses {
			toolTotals[k] += v
		}

		slug := s.Slug
		if slug == "" && len(s.SessionID) >= 8 {
			slug = s.SessionID[:8]
		} else if slug == "" {
			slug = s.SessionID
		}
		compactStr := ""
		if s.Compactions > 0 {
			compactStr = fmt.Sprintf("  compact:%d", s.Compactions)
		}
		fmt.Printf("%-28s  in:%-8s out:%-7s cache_r:%-8s cache_w:%-8s  tools:%-4d  msgs:%d/%d  %s%s\n",
			slug,
			fmtTokens(s.InputTokens),
			fmtTokens(s.OutputTokens),
			fmtTokens(s.CacheReadTokens),
			fmtTokens(s.CacheCreateTokens),
			s.TotalTool,
			s.UserMessages, s.AsstMessages,
			fmtDuration(s.Duration),
			compactStr,
		)
	}

	if len(stats) > 1 {
		fmt.Println(strings.Repeat("─", 120))
		fmt.Printf("%-28s  in:%-8s out:%-7s cache_r:%-8s cache_w:%-8s  tools:%-4d  msgs:%d/%d\n",
			fmt.Sprintf("TOTAL (%d sessions)", len(stats)),
			fmtTokens(totalIn),
			fmtTokens(totalOut),
			fmtTokens(totalCacheRead),
			fmtTokens(totalCacheCreate),
			totalToolUse,
			totalUser, totalAsst,
		)

		// Top tools.
		type kv struct {
			k string
			v int
		}
		var sorted []kv
		for k, v := range toolTotals {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
		fmt.Println("\nTool usage:")
		for _, t := range sorted {
			fmt.Printf("  %5d  %s\n", t.v, t.k)
		}
	}
	return nil
}

func fmtTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]
	var num int
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
		return time.ParseDuration(s)
	}
	switch suffix {
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour, nil
	default:
		return time.ParseDuration(s)
	}
}
