// Command cccat filters and displays Claude Code session entries.
//
// It reads JSONL session files from stdin or file arguments and outputs
// entries matching the given filters. It works like grep but understands
// session structure.
//
// Usage:
//
//	cccat [flags] [file...]
//	ccsessions -format jsonl | cccat -type user
//
// Examples:
//
//	cccat -role user session.jsonl
//	cccat -role assistant -text session.jsonl
//	cccat -tool Bash session.jsonl
//	cccat -type summary session.jsonl
//	cccat -role assistant -tool-names session.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tmc/cc"
)

var (
	typeFlag      = flag.String("type", "", "Filter by entry type (user, assistant, system, summary, progress)")
	roleFlag      = flag.String("role", "", "Filter by message role (user, assistant)")
	toolFlag      = flag.String("tool", "", "Filter tool_use blocks by tool name")
	textFlag      = flag.Bool("text", false, "Output only text content (no JSON)")
	toolNamesFlag = flag.Bool("tool-names", false, "Output only tool names used")
	formatFlag    = flag.String("format", "", "Output format: json, jsonl, text (default: auto)")
	grepFlag      = flag.String("grep", "", "Filter entries containing substring")
	countFlag     = flag.Bool("c", false, "Output count of matching entries")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cccat: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	readers, err := inputs()
	if err != nil {
		return err
	}

	var count int
	for _, r := range readers {
		rd := cc.NewReader(r)
		for rd.Next() {
			e := rd.Entry()
			if !match(e) {
				continue
			}
			count++
			if *countFlag {
				continue
			}
			if err := output(e); err != nil {
				return err
			}
		}
		if rd.Err() != nil {
			return rd.Err()
		}
		if c, ok := r.(io.Closer); ok && c != os.Stdin {
			c.Close()
		}
	}

	if *countFlag {
		fmt.Println(count)
	}
	return nil
}

func inputs() ([]io.Reader, error) {
	args := flag.Args()
	if len(args) == 0 {
		return []io.Reader{os.Stdin}, nil
	}
	var readers []io.Reader
	for _, path := range args {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		readers = append(readers, f)
	}
	return readers, nil
}

func match(e cc.Entry) bool {
	if *typeFlag != "" && e.Type != *typeFlag {
		return false
	}
	if *roleFlag != "" {
		if e.Message == nil || e.Message.Role != *roleFlag {
			return false
		}
	}
	if *toolFlag != "" {
		if e.Message == nil {
			return false
		}
		found := false
		for _, tu := range e.Message.ToolUses() {
			if strings.EqualFold(tu.Name, *toolFlag) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if *grepFlag != "" {
		text := entryText(e)
		if !strings.Contains(strings.ToLower(text), strings.ToLower(*grepFlag)) {
			return false
		}
	}
	return true
}

func entryText(e cc.Entry) string {
	if e.Message != nil {
		return e.Message.TextContent()
	}
	if e.Content != "" {
		return e.Content
	}
	if e.Summary != "" {
		return e.Summary
	}
	return ""
}

func output(e cc.Entry) error {
	if *toolNamesFlag {
		if e.Message == nil {
			return nil
		}
		for _, tu := range e.Message.ToolUses() {
			fmt.Println(tu.Name)
		}
		return nil
	}

	if *textFlag {
		// Label compact entries distinctly.
		if e.Type == "system" && e.Subtype == "compact_boundary" {
			trigger := ""
			if e.CompactMetadata != nil {
				trigger = fmt.Sprintf(" (%s, %d tokens)", e.CompactMetadata.Trigger, e.CompactMetadata.PreTokens)
			}
			fmt.Printf("[compacted]%s\n", trigger)
			return nil
		}
		if e.IsCompactSummary {
			text := entryText(e)
			if text != "" {
				fmt.Printf("[compact-summary] %s\n", text)
			}
			return nil
		}

		text := entryText(e)
		if text != "" {
			if e.Message != nil {
				fmt.Printf("[%s] %s\n", e.Message.Role, text)
			} else {
				fmt.Printf("[%s] %s\n", e.Type, text)
			}
		}
		return nil
	}

	format := *formatFlag
	if format == "" {
		format = "jsonl"
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(e)
	default: // jsonl
		enc := json.NewEncoder(os.Stdout)
		return enc.Encode(e)
	}
}
