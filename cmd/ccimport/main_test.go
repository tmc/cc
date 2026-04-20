package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
)

// TestToGeminiMessagesDrops asserts that tool_use, tool_result, and
// subagent entries are counted in dropCounts and reported to stderr,
// and that assistant token usage round-trips into geminiChatMessage.Tokens.
func TestToGeminiMessagesDrops(t *testing.T) {
	ts := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	entries := []cc.Entry{
		{
			UUID:      "u1",
			Timestamp: ts,
			Message: &cc.Message{
				Role:    "user",
				Content: json.RawMessage(`"hello"`),
			},
		},
		{
			UUID:      "u2",
			Timestamp: ts,
			Message: &cc.Message{
				Role:    "assistant",
				Model:   "claude-opus-4-7",
				Content: json.RawMessage(`[{"type":"text","text":"reply"},{"type":"tool_use","name":"Bash","id":"t1"}]`),
				Usage: &cc.Usage{
					InputTokens:          100,
					OutputTokens:         50,
					CacheReadInputTokens: 25,
				},
			},
		},
		{
			UUID:      "u3",
			Timestamp: ts,
			Message: &cc.Message{
				Role:    "user",
				Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]`),
			},
		},
		{
			UUID:        "u4",
			Timestamp:   ts,
			IsSidechain: true,
			Message: &cc.Message{
				Role:    "assistant",
				Content: json.RawMessage(`"subagent output"`),
			},
		},
	}

	msgs, drops := toGeminiMessages(entries)

	if drops.toolUses != 1 {
		t.Errorf("toolUses = %d, want 1", drops.toolUses)
	}
	if drops.toolResults != 1 {
		t.Errorf("toolResults = %d, want 1", drops.toolResults)
	}
	if drops.subagents != 1 {
		t.Errorf("subagents = %d, want 1", drops.subagents)
	}

	var asst *geminiChatMessage
	for i := range msgs {
		if msgs[i].Type == "gemini" {
			asst = &msgs[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant message emitted")
	}
	if asst.Tokens.Input != 100 || asst.Tokens.Output != 50 || asst.Tokens.Cached != 25 {
		t.Errorf("tokens = %+v, want {100 50 25}", asst.Tokens)
	}

	var buf bytes.Buffer
	drops.warn(&buf)
	out := buf.String()
	for _, want := range []string{"1 tool_use", "1 tool_result", "1 subagent"} {
		if !strings.Contains(out, want) {
			t.Errorf("warn output missing %q; got:\n%s", want, out)
		}
	}
}
