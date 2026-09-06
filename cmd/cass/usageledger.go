package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/ccpaths"
)

// ledgerEntry is the part of one transcript line the unfolded sum needs.
type ledgerEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Message   *struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			OutputTokens             int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// transcriptSums walks the Claude Code transcripts and returns, per local day,
// the tokens summed one streamed entry at a time, along with the oldest file on
// disk.
//
// Claude Code splits an API message across one entry per streamed block, each
// repeating that message's prompt counts, so this sum counts a message's prompt
// once per block. It is not what a request cost — that is what the index
// stores — but it is what a reader that does not fold by message id arrives at,
// and the cross-check needs it to say which of the two the ledger is.
func transcriptSums(ctx context.Context, loc *time.Location) (byDay map[string]map[string]int, oldest time.Time, err error) {
	home, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, time.Time{}, err
	}
	byDay = make(map[string]map[string]int)
	root := filepath.Join(home, "projects")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil //nolint:nilerr // an unreadable corner of the tree is not fatal here
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if info, err := d.Info(); err == nil {
			if mod := info.ModTime(); oldest.IsZero() || mod.Before(oldest) {
				oldest = mod
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if !bytes.Contains(line, []byte(`"usage"`)) {
				continue
			}
			var e ledgerEntry
			if json.Unmarshal(line, &e) != nil || e.Message == nil || e.Message.Usage == nil {
				continue
			}
			u := e.Message.Usage
			day := e.Timestamp.In(loc).Format("2006-01-02")
			m := byDay[day]
			if m == nil {
				m = make(map[string]int)
				byDay[day] = m
			}
			m[e.Message.Model] += u.InputTokens + u.CacheReadInputTokens +
				u.CacheCreationInputTokens + u.OutputTokens
		}
		return nil
	})
	if err != nil {
		return nil, time.Time{}, err
	}
	return byDay, oldest, nil
}
