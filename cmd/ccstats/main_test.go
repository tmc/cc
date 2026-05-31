package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ccpkg "github.com/tmc/cc"
)

func TestStatsForFileCountsMessagesToolsTokensAndCompactions(t *testing.T) {
	path := writeStatsFixture(t)

	got, err := statsForFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", got.SessionID)
	}
	if got.Slug != "session-123" {
		t.Fatalf("slug = %q, want session-123", got.Slug)
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q, want claude-sonnet-4-6", got.Model)
	}
	if got.TotalEntries != 4 {
		t.Fatalf("total entries = %d, want 4", got.TotalEntries)
	}
	if got.UserMessages != 1 {
		t.Fatalf("user messages = %d, want 1", got.UserMessages)
	}
	if got.AsstMessages != 1 {
		t.Fatalf("assistant messages = %d, want 1", got.AsstMessages)
	}
	if got.Compactions != 1 {
		t.Fatalf("compactions = %d, want 1", got.Compactions)
	}
	if got.InputTokens != 111 || got.OutputTokens != 22 || got.CacheReadTokens != 3 || got.CacheCreateTokens != 7 {
		t.Fatalf("tokens = %+v, want input=111 output=22 cache_read=3 cache_create=7", got)
	}
	if got.TotalTool != 1 {
		t.Fatalf("total tool uses = %d, want 1", got.TotalTool)
	}
	if got.ToolUses["Bash"] != 1 {
		t.Fatalf("tool uses = %#v, want Bash=1", got.ToolUses)
	}
	if got.Duration != 3*time.Minute {
		t.Fatalf("duration = %s, want 3m", got.Duration)
	}
}

func TestOutputAggregatesSessionsInTextMode(t *testing.T) {
	oldFormat := *formatFlag
	*formatFlag = "text"
	t.Cleanup(func() { *formatFlag = oldFormat })

	stats := []sessionStats{
		{
			SessionID:         "session-123",
			Slug:              "session-123",
			InputTokens:       111,
			OutputTokens:      22,
			CacheReadTokens:   3,
			CacheCreateTokens: 7,
			UserMessages:      1,
			AsstMessages:      1,
			TotalTool:         1,
			Duration:          2 * time.Minute,
			Compactions:       1,
			ToolUses:          map[string]int{"Bash": 1},
		},
		{
			SessionID:         "session-456",
			Slug:              "session-456",
			InputTokens:       10,
			OutputTokens:      5,
			CacheReadTokens:   1,
			CacheCreateTokens: 2,
			UserMessages:      2,
			AsstMessages:      3,
			TotalTool:         4,
			Duration:          30 * time.Second,
			ToolUses:          map[string]int{"Read": 4},
		},
	}

	got := captureStdout(t, func() error {
		return output(stats)
	})
	for _, want := range []string{
		"session-123",
		"session-456",
		"TOTAL (2 sessions)",
		"in:121",
		"out:27",
		"cache_r:4",
		"cache_w:9",
		"tools:5",
		"msgs:3/4",
		"Tool usage:",
		"  4  Read",
		"  1  Bash",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("text output missing %q:\n%s", want, got)
		}
	}
}

func writeStatsFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	toolInput := json.RawMessage(`{"command":"echo hello"}`)
	assistantContent, err := json.Marshal([]ccpkg.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "tool_use", Name: "Bash", Input: toolInput},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolResultContent, err := json.Marshal([]ccpkg.ContentBlock{
		{Type: "tool_result", ToolUseID: "toolu_1", Content: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries := []ccpkg.Entry{
		{
			Type:      "user",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "user",
				Content: json.RawMessage(`"hello"`),
			},
		},
		{
			Type:      "assistant",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 1, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "assistant",
				Model:   "claude-sonnet-4-6",
				Content: assistantContent,
				Usage: &ccpkg.Usage{
					InputTokens:              111,
					OutputTokens:             22,
					CacheReadInputTokens:     3,
					CacheCreationInputTokens: 7,
				},
			},
		},
		{
			Type:      "system",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 2, 0, 0, time.UTC),
			Subtype:   "compact_boundary",
		},
		{
			Type:      "user",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 3, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "user",
				Content: toolResultContent,
			},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("operation failed: %v", runErr)
	}
	return buf.String()
}
