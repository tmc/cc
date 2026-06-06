package main

import (
	"context"
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
	"github.com/tmc/cc/ccpaths"
)

var (
	writesFlag = flag.Bool("writes", false, "Show only write operations (Write, Edit, Bash writes)")
	readsFlag  = flag.Bool("reads", false, "Show only read operations")
	uniqueFlag = flag.Bool("unique", false, "Deduplicate and show only file paths")
	sinceFlag  = flag.String("since", "", "Find sessions modified within duration")
	formatFlag = flag.String("format", "text", "Output format: text, json, jsonl")
	countFlag  = flag.Bool("count", false, "Show file counts sorted by frequency")
)

// fileOp represents a file operation found in a session.
type fileOp struct {
	File      string `json:"file"`
	Op        string `json:"op"`   // write, edit, read, bash-write, bash-redirect, glob, grep
	Tool      string `json:"tool"` // source tool name
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

	var ops []fileOp
	for _, r := range readers {
		entries, err := entriesFromReader(r)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			continue
		}
		var sessionID, ts string
		for _, e := range entries {
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
	}

	// Filter.
	if *writesFlag {
		ops = filter(ops, func(o fileOp) bool {
			return o.Op == "write" || o.Op == "edit" || o.Op == "bash-write" || o.Op == "bash-redirect"
		})
	}
	if *readsFlag {
		ops = filter(ops, func(o fileOp) bool {
			return o.Op == "read"
		})
	}

	return output(ops)
}

func entriesFromReader(r io.Reader) ([]cc.Entry, error) {
	if f, ok := r.(*os.File); ok && f != os.Stdin {
		return cc.ReadFile(context.Background(), f.Name())
	}
	rd := cc.NewReader(context.Background(), r)
	var entries []cc.Entry
	for rd.Next() {
		entries = append(entries, rd.Entry())
	}
	return entries, rd.Err()
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

func extractOps(e cc.Entry) []fileOp {
	if e.Message == nil {
		return nil
	}

	var ops []fileOp

	for _, tu := range e.Message.ToolUses() {
		switch tu.Name {
		case "Write":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, fileOp{File: inp.FilePath, Op: "write", Tool: "Write"})
			}

		case "Edit":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, fileOp{File: inp.FilePath, Op: "edit", Tool: "Edit"})
			}

		case "Read":
			var inp struct {
				FilePath string `json:"file_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.FilePath != "" {
				ops = append(ops, fileOp{File: inp.FilePath, Op: "read", Tool: "Read"})
			}

		case "NotebookEdit":
			var inp struct {
				NotebookPath string `json:"notebook_path"`
			}
			json.Unmarshal(tu.Input, &inp)
			if inp.NotebookPath != "" {
				ops = append(ops, fileOp{File: inp.NotebookPath, Op: "edit", Tool: "NotebookEdit"})
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

func bashFileOps(cmd string) []fileOp {
	if cmd == "" {
		return nil
	}
	var ops []fileOp
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
		ops = append(ops, fileOp{File: file, Op: "bash-" + op, Tool: "Bash"})
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

func filter(ops []fileOp, fn func(fileOp) bool) []fileOp {
	var out []fileOp
	for _, o := range ops {
		if fn(o) {
			out = append(out, o)
		}
	}
	return out
}

func output(ops []fileOp) error {
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
