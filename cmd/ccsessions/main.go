package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	sinceFlag   = flag.String("since", "16h", "Only show sessions modified within duration (e.g. 16h, 7d)")
	projectFlag = flag.String("project", "", "Filter by project name substring")
	formatFlag  = flag.String("format", "text", "Output format: text, json, jsonl")
	nFlag       = flag.Int("n", 50, "Max sessions to show")
	verboseFlag = flag.Bool("v", false, "Show first user message")
	indexFlag   = flag.Bool("index", false, "Use sessions-index.json (faster, less detail)")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccsessions: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	since, err := parseDuration(*sinceFlag)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", *sinceFlag, err)
	}

	if *indexFlag {
		return runIndex(since)
	}
	return runFull(since)
}

func runIndex(since time.Duration) error {
	entries, err := cc.AllIndexEntries(since, *projectFlag)
	if err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Modified > entries[j].Modified
	})
	if len(entries) > *nFlag {
		entries = entries[:*nFlag]
	}

	switch *formatFlag {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "jsonl":
		enc := json.NewEncoder(os.Stdout)
		for _, e := range entries {
			enc.Encode(e)
		}
		return nil
	default:
		for _, e := range entries {
			ts := e.ModifiedTime().Local().Format("2006-01-02 15:04")
			proj := shortPath(e.ProjectPath)
			prompt := collapse(e.FirstPrompt, 120)
			if e.Summary != "" {
				prompt = collapse(e.Summary, 120)
			}
			fmt.Printf("%-16s  %-36s  %-30s  msgs:%-4d",
				ts, e.SessionID, proj, e.MessageCount)
			if e.GitBranch != "" {
				fmt.Printf("  [%s]", e.GitBranch)
			}
			fmt.Println()
			if *verboseFlag && prompt != "" {
				fmt.Printf("  → %s\n", prompt)
			}
		}
		return nil
	}
}

func runFull(since time.Duration) error {
	files, err := cc.FindSessionFiles(since, *projectFlag)
	if err != nil {
		return err
	}

	var sessions []cc.SessionSummary
	for _, f := range files {
		entries, err := cc.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f, err)
			continue
		}
		s := cc.Summarize(f, entries)
		if s.TotalLines == 0 {
			continue
		}
		sessions = append(sessions, s)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastTime.After(sessions[j].LastTime)
	})
	if len(sessions) > *nFlag {
		sessions = sessions[:*nFlag]
	}

	switch *formatFlag {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sessions)
	case "jsonl":
		enc := json.NewEncoder(os.Stdout)
		for _, s := range sessions {
			enc.Encode(s)
		}
		return nil
	default:
		for _, s := range sessions {
			ts := s.LastTime.Local().Format("2006-01-02 15:04")
			proj := shortPath(s.CWD)
			fmt.Printf("%-16s  %-36s  %-30s  msgs:%d/%d  lines:%d",
				ts, s.SessionID, proj, s.UserMessages, s.AsstMessages, s.TotalLines)
			if s.Compactions > 0 {
				fmt.Printf("  compact:%d", s.Compactions)
			}
			if s.Slug != "" {
				fmt.Printf("  [%s]", s.Slug)
			}
			if s.Model != "" {
				fmt.Printf("  %s", s.Model)
			}
			fmt.Println()
			if *verboseFlag && s.FirstPrompt != "" {
				fmt.Printf("  → %s\n", s.FirstPrompt)
			}
		}
		return nil
	}
}

func shortPath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func collapse(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
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
