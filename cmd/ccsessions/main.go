package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

var (
	sinceFlag   = flag.String("since", "16h", "Only show sessions modified within duration (e.g. 16h, 7d)")
	projectFlag = flag.String("project", "", "Filter by project name substring")
	formatFlag  = flag.String("format", "text", "Output format: text, json, jsonl")
	nFlag       = flag.Int("n", 50, "Max sessions to show")
	verboseFlag = flag.Bool("v", false, "Show first user message")
	indexFlag   = flag.Bool("index", false, "Use sessions-index.json (faster, less detail)")
	dirFlag     = flag.String("dir", "", "Only show sessions recorded in this directory")
	harnessFlag = flag.String("harness", "", "Only show sessions from this harness")
	waitingFlag = flag.Bool("waiting", false, "Only show sessions blocked at an unanswered question dialog")
)

// pendingBySession holds unanswered question texts, keyed by session ID, for
// the -waiting text output.
var pendingBySession = make(map[string][]string)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccsessions: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	harness, err := cleanHarness(*harnessFlag)
	if err != nil {
		return err
	}
	*harnessFlag = harness
	if *dirFlag != "" {
		abs, err := filepath.Abs(*dirFlag)
		if err != nil {
			return fmt.Errorf("resolve directory: %w", err)
		}
		*dirFlag = filepath.Clean(abs)
	}
	since, err := ccpaths.ParseDuration(*sinceFlag)
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
	entries = filterIndexEntries(entries, *dirFlag, *harnessFlag)

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
			proj := ccpaths.ShortPath(e.ProjectPath)
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
	files, err := cc.FindSessionFiles(context.Background(), since, *projectFlag)
	if err != nil {
		return err
	}

	var sessions []cc.SessionSummary
	for _, f := range files {
		if *harnessFlag != "" && cc.Harness(f) != *harnessFlag {
			continue
		}
		entries, err := cc.ReadFile(context.Background(), f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", f, err)
			continue
		}
		s := cc.Summarize(f, entries)
		if s.TotalLines == 0 {
			continue
		}
		if *waitingFlag {
			pending := cc.PendingQuestions(entries)
			if len(pending) == 0 {
				continue
			}
			for _, q := range pending {
				pendingBySession[s.SessionID] = append(pendingBySession[s.SessionID], questionTexts(q.Input)...)
			}
		}
		if *dirFlag != "" && !summaryHasDirectory(s, *dirFlag) {
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
			proj := ccpaths.ShortPath(s.CWD)
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
			for _, q := range pendingBySession[s.SessionID] {
				fmt.Printf("  ? %s\n", q)
			}
		}
		return nil
	}
}

func cleanHarness(harness string) (string, error) {
	harness = strings.ToLower(strings.TrimSpace(harness))
	switch harness {
	case "", "claude", "codex", "gemini", "opencode", "pi", "antigravity":
		return harness, nil
	default:
		return "", fmt.Errorf("unknown harness %q", harness)
	}
}

func filterIndexEntries(entries []cc.IndexEntry, dir, harness string) []cc.IndexEntry {
	out := entries[:0]
	for _, e := range entries {
		if harness != "" && cc.Harness(e.FullPath) != harness {
			continue
		}
		if dir != "" && filepath.Clean(e.ProjectPath) != dir {
			continue
		}
		out = append(out, e)
	}
	return out
}

func summaryHasDirectory(s cc.SessionSummary, dir string) bool {
	if filepath.Clean(s.CWD) == dir {
		return true
	}
	for _, cwd := range s.DistinctCWDs {
		if filepath.Clean(cwd) == dir {
			return true
		}
	}
	return false
}

func collapse(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// questionTexts extracts the question texts from an AskUserQuestion input,
// handling both Claude's inline array and Antigravity's double-encoded
// "questions" string.
func questionTexts(input json.RawMessage) []string {
	var w struct {
		Questions json.RawMessage `json:"questions"`
	}
	if json.Unmarshal(input, &w) != nil || len(w.Questions) == 0 {
		return nil
	}
	raw := w.Questions
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var qs []struct {
		Question string `json:"question"`
	}
	if json.Unmarshal(raw, &qs) != nil {
		return nil
	}
	var out []string
	for _, q := range qs {
		if q.Question != "" {
			out = append(out, q.Question)
		}
	}
	return out
}
