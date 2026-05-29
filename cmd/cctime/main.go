package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

var (
	toolsFlag = flag.Bool("tools", false, "Show tool uses inline")
	briefFlag = flag.Bool("brief", false, "One line per entry, minimal detail")
	sinceFlag = flag.String("since", "", "Find sessions modified within duration")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cctime: %v\n", err)
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

	for _, r := range readers {
		rd := cc.NewReader(context.Background(), r)
		var prevTime time.Time
		for rd.Next() {
			e := rd.Entry()
			printEntry(e, &prevTime)
		}
	}
	return nil
}

func printEntry(e cc.Entry, prevTime *time.Time) {
	ts := ""
	gap := ""
	if !e.Timestamp.IsZero() {
		ts = e.Timestamp.Local().Format("15:04:05")
		if !prevTime.IsZero() {
			d := e.Timestamp.Sub(*prevTime)
			if d > 30*time.Second {
				gap = fmt.Sprintf(" (+%s)", fmtDuration(d))
			}
		}
		*prevTime = e.Timestamp
	}

	// Compact boundary: show as visual divider.
	if e.Type == "system" && e.Subtype == "compact_boundary" {
		trigger := ""
		preTokens := ""
		if e.CompactMetadata != nil {
			trigger = e.CompactMetadata.Trigger
			preTokens = fmtN(e.CompactMetadata.PreTokens)
		}
		if *briefFlag {
			fmt.Printf("\033[90m%s\033[0m%s  \033[1;93m━ COMPACTED\033[0m (%s, %s tok)\n", ts, gap, trigger, preTokens)
		} else {
			fmt.Printf("\n\033[90m%s\033[0m%s  \033[1;93m━━━ COMPACTED (%s, %s tokens) ━━━\033[0m\n", ts, gap, trigger, preTokens)
		}
		return
	}

	// Compact summary: label differently from regular user messages.
	if e.IsCompactSummary && e.Message != nil {
		text := e.Message.TextContent()
		if *briefFlag {
			fmt.Printf("\033[90m%s\033[0m%s  \033[2;3mSUMMARY\033[0m  %s\n", ts, gap, truncate(text, 80))
		} else {
			fmt.Printf("\033[90m%s\033[0m%s  \033[2;3m  compact summary\033[0m\n", ts, gap)
			printWrapped(truncate(text, 200), 120)
		}
		return
	}

	switch e.Type {
	case "attachment":
		if e.Attachment == nil {
			return
		}
		text := attachmentText(e.Attachment)
		if text == "" {
			return
		}
		if *briefFlag {
			fmt.Printf("\033[90m%s\033[0m%s  \033[1mATTACH\033[0m  %s\n", ts, gap, truncate(text, 100))
		} else {
			fmt.Printf("\033[90m%s\033[0m%s  \033[1;90m• %s\033[0m\n", ts, gap, text)
		}
	case "user":
		if e.Message == nil {
			return
		}
		text := e.Message.TextContent()
		if text == "" {
			// Might be tool_result
			for _, b := range e.Message.ToolResults() {
				if b.Content != "" {
					text = truncate(b.Content, 80)
					break
				}
			}
			if text == "" {
				text = "(tool results)"
			}
		}
		if *briefFlag {
			fmt.Printf("\033[36m%s\033[0m%s  \033[1mUSER\033[0m  %s\n", ts, gap, truncate(text, 100))
		} else {
			fmt.Printf("\n\033[36m%s\033[0m%s  \033[1;36m▶ USER\033[0m\n", ts, gap)
			printWrapped(text, 120)
		}

	case "assistant":
		if e.Message == nil {
			return
		}
		blocks := e.Message.ContentBlocks()
		if blocks == nil {
			// Plain text content.
			text := e.Message.TextContent()
			if text != "" {
				if *briefFlag {
					fmt.Printf("\033[33m%s\033[0m%s  \033[1mASST\033[0m  %s\n", ts, gap, truncate(text, 100))
				} else {
					fmt.Printf("\033[33m%s\033[0m%s  \033[1;33m◀ ASSISTANT\033[0m\n", ts, gap)
					printWrapped(text, 120)
				}
			}
			return
		}

		var textParts []string
		var tools []string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					textParts = append(textParts, b.Text)
				}
			case "tool_use":
				tools = append(tools, fmtToolUse(b))
			}
		}

		if *briefFlag {
			parts := ""
			if len(textParts) > 0 {
				parts = truncate(strings.Join(textParts, " "), 60)
			}
			toolStr := ""
			if len(tools) > 0 {
				toolStr = fmt.Sprintf(" [%s]", strings.Join(tools, ", "))
			}
			fmt.Printf("\033[33m%s\033[0m%s  \033[1mASST\033[0m  %s%s\n", ts, gap, parts, toolStr)
		} else {
			fmt.Printf("\033[33m%s\033[0m%s  \033[1;33m◀ ASSISTANT\033[0m", ts, gap)
			if e.Message.Model != "" {
				fmt.Printf("  \033[2m(%s)\033[0m", shortModel(e.Message.Model))
			}
			fmt.Println()
			if len(textParts) > 0 {
				printWrapped(strings.Join(textParts, "\n"), 120)
			}
			if *toolsFlag {
				for _, t := range tools {
					fmt.Printf("    \033[35m⚡ %s\033[0m\n", t)
				}
			}
		}

		// Print usage.
		if e.Message.Usage != nil && !*briefFlag {
			u := e.Message.Usage
			if u.InputTokens > 0 || u.OutputTokens > 0 {
				fmt.Printf("    \033[2mtokens: in=%s out=%s", fmtN(u.InputTokens), fmtN(u.OutputTokens))
				if u.CacheReadInputTokens > 0 {
					fmt.Printf(" cache_read=%s", fmtN(u.CacheReadInputTokens))
				}
				fmt.Printf("\033[0m\n")
			}
		}

	case "summary":
		if *briefFlag {
			fmt.Printf("\033[90m%s\033[0m%s  \033[1mSUMMARY\033[0m\n", ts, gap)
		} else {
			fmt.Printf("\n\033[90m%s\033[0m%s  \033[1;90m━━━ SUMMARY ━━━\033[0m\n", ts, gap)
			if e.Summary != "" {
				printWrapped(e.Summary, 120)
			}
		}
	}
}

func attachmentText(a *cc.Attachment) string {
	switch a.Type {
	case "workflow_keyword_request":
		return "workflow requested"
	case "task_reminder":
		return "task reminder"
	case "deferred_tools_delta":
		var raw struct {
			AddedNames []string `json:"addedNames"`
		}
		if json.Unmarshal(a.Raw, &raw) == nil && len(raw.AddedNames) > 0 {
			return fmt.Sprintf("deferred tools +%d", len(raw.AddedNames))
		}
	}
	return ""
}

func fmtToolUse(b cc.ContentBlock) string {
	switch b.Name {
	case "Bash":
		var inp struct {
			Command     string `json:"command"`
			Description string `json:"description,omitempty"`
		}
		json.Unmarshal(b.Input, &inp)
		cmd := truncate(strings.Join(strings.Fields(inp.Command), " "), 80)
		return fmt.Sprintf("Bash: $ %s", cmd)
	case "Edit":
		var inp struct {
			FilePath string `json:"file_path"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Edit: %s", ccpaths.ShortPath(inp.FilePath))
	case "Write":
		var inp struct {
			FilePath string `json:"file_path"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Write: %s", ccpaths.ShortPath(inp.FilePath))
	case "Read":
		var inp struct {
			FilePath string `json:"file_path"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Read: %s", ccpaths.ShortPath(inp.FilePath))
	case "Grep":
		var inp struct {
			Pattern string `json:"pattern"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Grep: %q", inp.Pattern)
	case "Glob":
		var inp struct {
			Pattern string `json:"pattern"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Glob: %q", inp.Pattern)
	case "Task":
		var inp struct {
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("Task[%s]: %s", inp.SubagentType, inp.Description)
	case "Workflow":
		var inp struct {
			Script     string `json:"script"`
			ScriptPath string `json:"scriptPath"`
		}
		json.Unmarshal(b.Input, &inp)
		name := workflowMetaField(inp.Script, "name")
		if name == "" {
			name = ccpaths.ShortPath(inp.ScriptPath)
		}
		return fmt.Sprintf("Workflow: %s", name)
	case "TaskCreate":
		var inp struct {
			Subject string `json:"subject"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("TaskCreate: %s", truncate(inp.Subject, 60))
	case "TaskUpdate":
		var inp struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		}
		json.Unmarshal(b.Input, &inp)
		return fmt.Sprintf("TaskUpdate: #%s %s", inp.TaskID, inp.Status)
	default:
		return b.Name
	}
}

func workflowMetaField(script, field string) string {
	re := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(field) + `\s*:\s*['"]([^'"]+)['"]`)
	m := re.FindStringSubmatch(script)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func printWrapped(s string, width int) {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	for _, line := range strings.Split(s, "\n") {
		fmt.Printf("    %s\n", line)
	}
}

func shortModel(m string) string {
	m = strings.TrimPrefix(m, "claude-")
	if i := strings.LastIndex(m, "-"); i > 10 {
		m = m[:i]
	}
	return m
}

func fmtN(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtDuration(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func inputs() ([]io.Reader, []io.Closer, error) {
	args := flag.Args()
	if *sinceFlag != "" && len(args) == 0 {
		since, err := ccpaths.ParseDuration(*sinceFlag)
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
