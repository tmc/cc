// Command ccfiles extracts file operations from Claude Code sessions.
//
// It finds all files that were written, edited, read, or created during a
// session, including those modified via Bash commands (redirects, tee, cp, mv).
//
// Usage:
//
//	ccfiles [flags] [file...]
//	ccfiles session.jsonl
//	ccfiles -writes session.jsonl
//	ccfiles -since 16h
//
// Examples:
//
//	ccfiles session.jsonl                    # all file ops
//	ccfiles -writes session.jsonl            # only writes/edits
//	ccfiles -reads session.jsonl             # only reads
//	ccfiles -unique session.jsonl            # deduplicated file list
//	ccfiles -writes -unique -since 16h       # all files written in last 16h
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	writesFlag = flag.Bool("writes", false, "Show only write operations (Write, Edit, Bash writes)")
	readsFlag  = flag.Bool("reads", false, "Show only read operations")
	uniqueFlag = flag.Bool("unique", false, "Deduplicate and show only file paths")
	sinceFlag  = flag.String("since", "", "Find sessions modified within duration")
	formatFlag = flag.String("format", "text", "Output format: text, json, jsonl")
	countFlag  = flag.Bool("count", false, "Show file counts sorted by frequency")
)

// FileOp represents a file operation found in a session.
type FileOp struct {
	File      string `json:"file"`
	Op        string `json:"op"`        // write, edit, read, bash-write, bash-redirect, glob, grep
	Tool      string `json:"tool"`      // source tool name
	SessionID string `json:"session_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccfiles: %v\n", err)
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

	var ops []FileOp
	for _, r := range readers {
		rd := cc.NewReader(r)
		var sessionID, ts string
		for rd.Next() {
			e := rd.Entry()
			if e.SessionID != "" {
				sessionID = e.SessionID
			}
			if !e.Timestamp.IsZero() {
				ts = e.Timestamp.Format(time.RFC3339)
			}
			found := extractOps(e)
			for i := range found {
				found[i].SessionID = sessionID
				found[i].Timestamp = ts
			}
			ops = append(ops, found...)
		}
		if rd.Err() != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", rd.Err())
		}
	}

	// Filter.
	if *writesFlag {
		ops = filter(ops, func(o FileOp) bool {
			return o.Op == "write" || o.Op == "edit" || o.Op == "bash-write" || o.Op == "bash-redirect"
		})
	}
	if *readsFlag {
		ops = filter(ops, func(o FileOp) bool {
			return o.Op == "read"
		})
	}

	return output(ops)
}

func inputs() ([]io.Reader, []io.Closer, error) {
	args := flag.Args()

	if *sinceFlag != "" && len(args) == 0 {
		since, err := parseDuration(*sinceFlag)
		if err != nil {
			return nil, nil, err
		}
		files, err := cc.FindSessionFiles(since, "")
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
	for _, path := range args {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}
	return readers, closers, nil
}

func extractOps(e cc.Entry) []FileOp {
	if e.Message == nil {
		return nil
	}

	var ops []FileOp

	for _, tu := range e.Message.ToolUses() {
		switch tu.Name {
		case "Write":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, FileOp{File: inp.FilePath, Op: "write", Tool: "Write"})
			}

		case "Edit":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, FileOp{File: inp.FilePath, Op: "edit", Tool: "Edit"})
			}

		case "Read":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, FileOp{File: inp.FilePath, Op: "read", Tool: "Read"})
			}

		case "NotebookEdit":
			var inp struct {
				NotebookPath string `json:"notebook_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.NotebookPath != "" {
				ops = append(ops, FileOp{File: inp.NotebookPath, Op: "edit", Tool: "NotebookEdit"})
			}

		case "Bash":
			var inp struct {
				Command string `json:"command"`
			}
			json.Unmarshal(tu.Input, &inp)
			ops = append(ops, bashFileOps(inp.Command)...)
		}
	}
	return ops
}

// Patterns for extracting file paths from bash commands.
var (
	// Redirects: cmd > file, cmd >> file (but not 2>&1 or >&2)
	redirectRe = regexp.MustCompile(`(?:^|[^&0-9])>+\s+([a-zA-Z_~/.\-][^\s;|&]*)`)
	// tee: cmd | tee [-a] file
	teeRe = regexp.MustCompile(`tee\s+(?:-a\s+)?(\S+)`)
	// cp/mv: cp src dst, mv src dst
	cpMvRe = regexp.MustCompile(`(?:cp|mv)\s+(?:-[a-zA-Z]+\s+)*\S+\s+(\S+)`)
	// cat/echo heredoc: cat > file <<, cat >> file <<
	heredocRe = regexp.MustCompile(`cat\s+>+\s*(\S+)\s*<<`)
	// sed -i: sed -i '' 's/...' file
	sedRe = regexp.MustCompile(`sed\s+-i['"e]?\s+(?:'[^']*'\s+)?(\S+)`)
	// touch/mkdir (creates)
	touchRe = regexp.MustCompile(`touch\s+(\S+)`)
	// rm
	rmRe = regexp.MustCompile(`rm\s+(?:-[a-zA-Z]+\s+)*(\S+)`)
	// perl -pi -e
	perlRe = regexp.MustCompile(`perl\s+-p?i[e]?\s+(?:-e\s+)?'[^']*'\s+(\S+)`)
)

func bashFileOps(cmd string) []FileOp {
	if cmd == "" {
		return nil
	}
	var ops []FileOp
	seen := make(map[string]bool)

	add := func(file, op string) {
		// Skip common non-file args.
		if file == "" || file == "-" || file == "." || file == "/dev/null" || file == "/dev/stderr" || file == "/dev/stdout" {
			return
		}
		// Skip flags.
		if strings.HasPrefix(file, "-") {
			return
		}
		key := file + ":" + op
		if seen[key] {
			return
		}
		seen[key] = true
		ops = append(ops, FileOp{File: file, Op: "bash-" + op, Tool: "Bash"})
	}

	for _, m := range redirectRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "redirect")
	}
	for _, m := range teeRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}
	for _, m := range cpMvRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}
	for _, m := range heredocRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "redirect")
	}
	for _, m := range sedRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}
	for _, m := range touchRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}
	for _, m := range perlRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}
	for _, m := range rmRe.FindAllStringSubmatch(cmd, -1) {
		add(m[1], "write")
	}

	return ops
}

func filter(ops []FileOp, fn func(FileOp) bool) []FileOp {
	var out []FileOp
	for _, o := range ops {
		if fn(o) {
			out = append(out, o)
		}
	}
	return out
}

func output(ops []FileOp) error {
	if *countFlag {
		counts := make(map[string]int)
		for _, o := range ops {
			counts[o.File]++
		}
		type kv struct {
			k string
			v int
		}
		var sorted []kv
		for k, v := range counts {
			sorted = append(sorted, kv{k, v})
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].v > sorted[j].v })
		for _, t := range sorted {
			fmt.Printf("%5d  %s\n", t.v, t.k)
		}
		return nil
	}

	if *uniqueFlag {
		seen := make(map[string]bool)
		for _, o := range ops {
			if !seen[o.File] {
				seen[o.File] = true
				fmt.Println(o.File)
			}
		}
		return nil
	}

	switch *formatFlag {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ops)
	case "jsonl":
		enc := json.NewEncoder(os.Stdout)
		for _, o := range ops {
			enc.Encode(o)
		}
		return nil
	default:
		for _, o := range ops {
			fmt.Printf("%-14s  %-6s  %s\n", o.Tool, o.Op, o.File)
		}
		return nil
	}
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
