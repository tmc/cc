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
	pathFlag   = flag.String("path", "", "Filter by file path substring")
	sinceFlag  = flag.String("since", "", "Find sessions modified within duration")
	statFlag   = flag.Bool("stat", false, "Show diffstat summary only")
	writesFlag = flag.Bool("writes", false, "Include Write tool content")
)

type editOp struct {
	file      string
	oldString string
	newString string
	ts        string
}

type writeOp struct {
	file    string
	content string
	ts      string
}

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccdiff: %v\n", err)
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

	var edits []editOp
	var writes []writeOp

	for _, r := range readers {
		rd := cc.NewReader(context.Background(), r)
		var ts string
		for rd.Next() {
			e := rd.Entry()
			if !e.Timestamp.IsZero() {
				ts = e.Timestamp.Local().Format("15:04:05")
			}
			if e.Message == nil {
				continue
			}
			for _, tu := range e.Message.ToolUses() {
				switch tu.Name {
				case "Edit":
					var inp struct {
						FilePath  string `json:"file_path"`
						OldString string `json:"old_string"`
						NewString string `json:"new_string"`
					}
					json.Unmarshal(tu.Input, &inp)
					if inp.FilePath == "" {
						continue
					}
					if *pathFlag != "" && !strings.Contains(inp.FilePath, *pathFlag) {
						continue
					}
					edits = append(edits, editOp{
						file:      inp.FilePath,
						oldString: inp.OldString,
						newString: inp.NewString,
						ts:        ts,
					})
				case "Write":
					if !*writesFlag {
						continue
					}
					var inp struct {
						FilePath string `json:"file_path"`
						Content  string `json:"content"`
					}
					json.Unmarshal(tu.Input, &inp)
					if inp.FilePath == "" {
						continue
					}
					if *pathFlag != "" && !strings.Contains(inp.FilePath, *pathFlag) {
						continue
					}
					writes = append(writes, writeOp{
						file:    inp.FilePath,
						content: inp.Content,
						ts:      ts,
					})
				}
			}
		}
	}

	if *statFlag {
		return printStat(edits, writes)
	}
	return printDiffs(edits, writes)
}

func printStat(edits []editOp, writes []writeOp) error {
	// Group by file.
	fileCounts := make(map[string][2]int) // [edits, added_lines]
	for _, e := range edits {
		c := fileCounts[e.file]
		c[0]++
		oldLines := strings.Count(e.oldString, "\n")
		newLines := strings.Count(e.newString, "\n")
		c[1] += newLines - oldLines
		fileCounts[e.file] = c
	}
	for _, w := range writes {
		c := fileCounts[w.file]
		c[0]++
		c[1] += strings.Count(w.content, "\n") + 1
		fileCounts[w.file] = c
	}

	for f, c := range fileCounts {
		sign := "+"
		delta := c[1]
		if delta < 0 {
			sign = ""
		}
		fmt.Printf(" %-60s | %3d edits  %s%d lines\n", cc.ShortPath(f), c[0], sign, delta)
	}
	fmt.Printf(" %d files changed, %d edits, %d writes\n", len(fileCounts), len(edits), len(writes))
	return nil
}

func printDiffs(edits []editOp, writes []writeOp) error {
	for _, e := range edits {
		fmt.Printf("\033[1m--- %s\033[0m\n", cc.ShortPath(e.file))
		fmt.Printf("\033[1m+++ %s\033[0m  (%s)\n", cc.ShortPath(e.file), e.ts)
		oldLines := strings.Split(e.oldString, "\n")
		newLines := strings.Split(e.newString, "\n")
		for _, l := range oldLines {
			fmt.Printf("\033[31m-%s\033[0m\n", l)
		}
		for _, l := range newLines {
			fmt.Printf("\033[32m+%s\033[0m\n", l)
		}
		fmt.Println()
	}
	for _, w := range writes {
		fmt.Printf("\033[1m+++ %s\033[0m (new file, %s)\n", cc.ShortPath(w.file), w.ts)
		lines := strings.Split(w.content, "\n")
		if len(lines) > 20 {
			for _, l := range lines[:20] {
				fmt.Printf("\033[32m+%s\033[0m\n", l)
			}
			fmt.Printf("\033[2m... +%d more lines\033[0m\n", len(lines)-20)
		} else {
			for _, l := range lines {
				fmt.Printf("\033[32m+%s\033[0m\n", l)
			}
		}
		fmt.Println()
	}
	return nil
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
