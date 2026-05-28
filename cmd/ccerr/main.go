// Command ccerr finds errors, failures, and retries in Claude Code sessions.
//
// It scans for tool errors, bash failures, API errors, and user rejections.
//
// Usage:
//
//	ccerr [flags] [file...]
//	ccerr session.jsonl
//	ccerr -since 16h
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tmc/cc"
)

var (
	sinceFlag   = flag.String("since", "", "Find sessions modified within duration")
	verboseFlag = flag.Bool("v", false, "Show full error content")
	countFlag   = flag.Bool("c", false, "Show error count only")
	formatFlag  = flag.String("format", "text", "Output format: text, json")
)

// ErrRecord holds one detected error.
type ErrRecord struct {
	Time      string `json:"time,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Kind      string `json:"kind"` // tool-error, bash-fail, api-error, user-reject, interrupted
	Tool      string `json:"tool,omitempty"`
	Message   string `json:"message"`
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccerr: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	readers, closers, err := inputs()
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()

	var errs []ErrRecord
	for _, r := range readers {
		rd := cc.NewReader(context.Background(), r)
		var sessionID, ts string
		for rd.Next() {
			e := rd.Entry()
			if e.SessionID != "" {
				sessionID = e.SessionID
			}
			if !e.Timestamp.IsZero() {
				ts = e.Timestamp.Local().Format("15:04:05")
			}
			found := findErrors(e)
			for i := range found {
				found[i].SessionID = sessionID
				found[i].Time = ts
			}
			errs = append(errs, found...)
		}
	}

	if *countFlag {
		// Count by kind.
		counts := make(map[string]int)
		for _, e := range errs {
			counts[e.Kind]++
		}
		total := 0
		for k, v := range counts {
			fmt.Printf("  %5d  %s\n", v, k)
			total += v
		}
		fmt.Printf("  %5d  total\n", total)
		return nil
	}

	if *formatFlag == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(errs)
	}

	for _, e := range errs {
		msg := e.Message
		if !*verboseFlag && len(msg) > 120 {
			msg = msg[:120] + "..."
		}
		tool := ""
		if e.Tool != "" {
			tool = fmt.Sprintf(" (%s)", e.Tool)
		}
		fmt.Printf("\033[31m%s\033[0m  \033[1m%-15s\033[0m%s  %s\n", e.Time, e.Kind, tool, msg)
	}
	return nil
}

func findErrors(e cc.Entry) []ErrRecord {
	var errs []ErrRecord

	// Check for user rejections / interruptions.
	if e.Type == "user" && e.Message != nil {
		text := e.Message.TextContent()
		if strings.Contains(text, "tool use was rejected") {
			errs = append(errs, ErrRecord{Kind: "user-reject", Message: truncate(text, 200)})
		}
		if strings.Contains(text, "Request interrupted") {
			errs = append(errs, ErrRecord{Kind: "interrupted", Message: truncate(text, 200)})
		}
	}

	// Check tool results for errors.
	if e.ToolUseResult != nil {
		r := e.ToolUseResult
		if r.Error != "" {
			errs = append(errs, ErrRecord{Kind: "tool-error", Message: r.Error})
		}
		if r.Stderr != "" && containsError(r.Stderr) {
			errs = append(errs, ErrRecord{Kind: "bash-fail", Tool: "Bash", Message: truncate(r.Stderr, 200)})
		}
	}

	// Check assistant messages for tool_use that get error results back.
	if e.Type == "user" && e.Message != nil {
		for _, b := range e.Message.ContentBlocks() {
			if b.Type == "tool_result" && b.IsError {
				errs = append(errs, ErrRecord{
					Kind:    "tool-error",
					Tool:    b.ToolUseID,
					Message: truncate(b.Content, 200),
				})
			}
		}
	}

	// Check for API errors.
	if e.Type == "system" && e.Content != "" && containsError(e.Content) {
		errs = append(errs, ErrRecord{Kind: "api-error", Message: truncate(e.Content, 200)})
	}

	return errs
}

func containsError(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "error") ||
		strings.Contains(lower, "fail") ||
		strings.Contains(lower, "panic") ||
		strings.Contains(lower, "fatal") ||
		strings.Contains(lower, "denied") ||
		strings.Contains(lower, "refused")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func inputs() ([]io.Reader, []io.Closer, error) {
	args := flag.Args()
	if *sinceFlag != "" && len(args) == 0 {
		since, err := cc.ParseDuration(*sinceFlag)
		if err != nil {
			return nil, nil, err
		}
		files, err := cc.FindSessionFiles(context.Background(), since, "")
		if err != nil {
			return nil, nil, err
		}
		args = files
	}
	if len(args) == 0 {
		return []io.Reader{os.Stdin}, nil, nil
	}
	var readers []io.Reader
	var closers []io.Closer
	for _, p := range args {
		f, err := os.Open(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", p, err)
			continue
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}
	return readers, closers, nil
}
