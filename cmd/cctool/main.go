// Command cctool extracts tool use details from Claude Code sessions.
//
// It shows what tools were invoked, with what arguments, enabling review
// of bash commands run, files edited, searches performed, etc.
//
// Usage:
//
//	cctool [flags] [file...]
//	cctool -name Bash session.jsonl
//	cctool -name Edit -show-input session.jsonl
//
// Examples:
//
//	cctool -name Bash session.jsonl            # show all bash commands
//	cctool -name Edit session.jsonl            # show all file edits
//	cctool -names session.jsonl                # list tool names used
//	cctool -name Write -show-input session.jsonl  # show file writes with content
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
	nameFlag      = flag.String("name", "", "Filter by tool name (e.g. Bash, Edit, Read, Write, Grep, Glob)")
	namesFlag     = flag.Bool("names", false, "List distinct tool names with counts")
	showInputFlag = flag.Bool("show-input", false, "Show full tool input JSON")
	compactFlag   = flag.Bool("compact", false, "One-line output per tool use")
)

// BashInput is the parsed input for Bash tool calls.
type BashInput struct {
	Command     string `json:"command"`
	Description string `json:"description,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
}

// EditInput is the parsed input for Edit tool calls.
type EditInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// ReadInput is the parsed input for Read tool calls.
type ReadInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// WriteInput is the parsed input for Write tool calls.
type WriteInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// GrepInput is the parsed input for Grep tool calls.
type GrepInput struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
}

// GlobInput is the parsed input for Glob tool calls.
type GlobInput struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
}

// TaskInput is the parsed input for Task tool calls.
type TaskInput struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type,omitempty"`
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cctool: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	readers, err := inputs()
	if err != nil {
		return err
	}

	counts := make(map[string]int)

	for _, r := range readers {
		rd := cc.NewReader(r)
		for rd.Next() {
			e := rd.Entry()
			if e.Message == nil {
				continue
			}
			for _, tu := range e.Message.ToolUses() {
				counts[tu.Name]++
				if *namesFlag {
					continue
				}
				if *nameFlag != "" && !strings.EqualFold(tu.Name, *nameFlag) {
					continue
				}
				printToolUse(tu)
			}
		}
		if rd.Err() != nil {
			return rd.Err()
		}
		if c, ok := r.(io.Closer); ok && c != os.Stdin {
			c.Close()
		}
	}

	if *namesFlag {
		type kv struct {
			k string
			v int
		}
		var sorted []kv
		for k, v := range counts {
			sorted = append(sorted, kv{k, v})
		}
		for i := 0; i < len(sorted); i++ {
			for j := i + 1; j < len(sorted); j++ {
				if sorted[j].v > sorted[i].v {
					sorted[i], sorted[j] = sorted[j], sorted[i]
				}
			}
		}
		for _, t := range sorted {
			fmt.Printf("%5d  %s\n", t.v, t.k)
		}
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

func printToolUse(tu cc.ContentBlock) {
	if *showInputFlag {
		fmt.Printf("── %s ──\n", tu.Name)
		var pretty json.RawMessage
		if json.Unmarshal(tu.Input, &pretty) == nil {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(pretty)
		}
		fmt.Println()
		return
	}

	switch tu.Name {
	case "Bash":
		var inp BashInput
		json.Unmarshal(tu.Input, &inp)
		if *compactFlag {
			cmd := strings.Join(strings.Fields(inp.Command), " ")
			if len(cmd) > 120 {
				cmd = cmd[:120] + "..."
			}
			fmt.Printf("$ %s\n", cmd)
		} else {
			if inp.Description != "" {
				fmt.Printf("# %s\n", inp.Description)
			}
			fmt.Printf("$ %s\n\n", inp.Command)
		}

	case "Edit":
		var inp EditInput
		json.Unmarshal(tu.Input, &inp)
		if *compactFlag {
			fmt.Printf("edit %s\n", inp.FilePath)
		} else {
			fmt.Printf("── Edit %s ──\n", inp.FilePath)
			fmt.Printf("- %s\n+ %s\n\n", truncLines(inp.OldString, 3), truncLines(inp.NewString, 3))
		}

	case "Write":
		var inp WriteInput
		json.Unmarshal(tu.Input, &inp)
		if *compactFlag {
			fmt.Printf("write %s (%d bytes)\n", inp.FilePath, len(inp.Content))
		} else {
			fmt.Printf("── Write %s (%d bytes) ──\n\n", inp.FilePath, len(inp.Content))
		}

	case "Read":
		var inp ReadInput
		json.Unmarshal(tu.Input, &inp)
		fmt.Printf("read %s", inp.FilePath)
		if inp.Offset > 0 || inp.Limit > 0 {
			fmt.Printf(" [offset:%d limit:%d]", inp.Offset, inp.Limit)
		}
		fmt.Println()

	case "Grep":
		var inp GrepInput
		json.Unmarshal(tu.Input, &inp)
		fmt.Printf("grep %q", inp.Pattern)
		if inp.Path != "" {
			fmt.Printf(" %s", inp.Path)
		}
		if inp.Glob != "" {
			fmt.Printf(" --glob %s", inp.Glob)
		}
		fmt.Println()

	case "Glob":
		var inp GlobInput
		json.Unmarshal(tu.Input, &inp)
		fmt.Printf("glob %q", inp.Pattern)
		if inp.Path != "" {
			fmt.Printf(" %s", inp.Path)
		}
		fmt.Println()

	case "Task":
		var inp TaskInput
		json.Unmarshal(tu.Input, &inp)
		if *compactFlag {
			fmt.Printf("task [%s] %s\n", inp.SubagentType, inp.Description)
		} else {
			fmt.Printf("── Task [%s] %s ──\n", inp.SubagentType, inp.Description)
			if inp.Prompt != "" {
				p := inp.Prompt
				if len(p) > 200 {
					p = p[:200] + "..."
				}
				fmt.Printf("%s\n\n", p)
			}
		}

	default:
		if *compactFlag {
			fmt.Printf("%s\n", tu.Name)
		} else {
			fmt.Printf("── %s ──\n\n", tu.Name)
		}
	}
}

func truncLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = append(lines[:n], "...")
	}
	return strings.Join(lines, "\n")
}
