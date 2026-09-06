package collector

import (
	"encoding/json"
	"testing"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccusage"
	"github.com/tmc/tokencount/anthropictokenizer"
)

// testCounter is the BPE counter the estimate uses, built independently of
// ccusage so the replica below cannot agree with ExtractStats by sharing its
// implementation.
func testCounter(t *testing.T) *anthropictokenizer.Counter {
	t.Helper()
	c, err := anthropictokenizer.NewCounter()
	if err != nil {
		t.Skipf("tokenizer unavailable: %v", err)
	}
	return c
}

// assistantWithUsage builds an assistant entry with the given text and token
// counts, as a session file's assistant line decodes to.
func assistantWithUsage(text string, in, cacheRead, cacheCreate, out int) cc.Entry {
	content, err := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	if err != nil {
		panic(err)
	}
	return cc.Entry{
		Type: "assistant",
		Message: &cc.Message{
			Role:    "assistant",
			Model:   "claude-opus-5",
			Content: content,
			Usage: &cc.Usage{
				InputTokens:              in,
				OutputTokens:             out,
				CacheReadInputTokens:     cacheRead,
				CacheCreationInputTokens: cacheCreate,
			},
		},
	}
}

// oldTokenTotals is the token arithmetic ExtractStats performed before the
// estimate moved into ccusage, kept here so the move stays provably
// behavior-preserving: it must agree with ExtractStats on every input.
func oldTokenTotals(t *testing.T, entries []cc.Entry) (in, out, cacheRead, cacheCreate int, estimated bool) {
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "assistant" || e.Message.Usage == nil {
			continue
		}
		in += e.Message.Usage.InputTokens
		o := e.Message.Usage.OutputTokens
		if o <= 1 {
			if text := e.Message.TextContent(); text != "" {
				o = testCounter(t).Count(text)
				estimated = true
			}
		}
		out += o
		cacheRead += e.Message.Usage.CacheReadInputTokens
		cacheCreate += e.Message.Usage.CacheCreationInputTokens
	}
	return
}

// TestExtractStatsTokenTotals pins the five token fields ExtractStats reports.
// They are persisted in stats_json and the index has no backfill, so a change
// in this arithmetic silently disagrees with every session already indexed.
func TestExtractStatsTokenTotals(t *testing.T) {
	tests := []struct {
		name    string
		entries []cc.Entry
	}{
		{
			name: "reported output",
			entries: []cc.Entry{
				assistantWithUsage("hello", 10, 100, 20, 50),
				assistantWithUsage("world", 5, 200, 0, 30),
			},
		},
		{
			// Output 1 is the streaming-start snapshot, so it is replaced by a
			// BPE count of the text and the session is flagged estimated.
			name: "snapshot output estimated",
			entries: []cc.Entry{
				assistantWithUsage("a longer answer than one token", 10, 100, 20, 1),
			},
		},
		{
			// No text to tokenize: the reported count stands and the session
			// is not flagged.
			name: "snapshot output with no text",
			entries: []cc.Entry{
				assistantWithUsage("", 10, 100, 20, 1),
			},
		},
		{
			name: "mixed",
			entries: []cc.Entry{
				assistantWithUsage("some text here", 10, 100, 20, 1),
				assistantWithUsage("more text", 7, 0, 0, 42),
				{Type: "user", Message: &cc.Message{Role: "user"}},
				{Type: "assistant", Message: &cc.Message{Role: "assistant"}},
			},
		},
		{
			name:    "no entries",
			entries: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ExtractStats(tt.entries)
			in, out, cacheRead, cacheCreate, estimated := oldTokenTotals(t, tt.entries)
			if s.InputTokens != in {
				t.Errorf("InputTokens = %d, want %d", s.InputTokens, in)
			}
			if s.OutputTokensSnapshot != out {
				t.Errorf("OutputTokensSnapshot = %d, want %d", s.OutputTokensSnapshot, out)
			}
			if s.CacheReads != cacheRead {
				t.Errorf("CacheReads = %d, want %d", s.CacheReads, cacheRead)
			}
			if s.CacheCreationInputTokens != cacheCreate {
				t.Errorf("CacheCreationInputTokens = %d, want %d", s.CacheCreationInputTokens, cacheCreate)
			}
			if s.OutputTokensEstimated != estimated {
				t.Errorf("OutputTokensEstimated = %v, want %v", s.OutputTokensEstimated, estimated)
			}
		})
	}
}

// TestExtractStatsSumsPerEntry documents that ExtractStats sums every assistant
// entry, including the several Claude Code writes for one streamed message. It
// overcounts the prompt of such a message, which is why ccusage.FromEntries
// folds by message id instead; the two are deliberately different, and this
// test exists so a future change to ExtractStats is a decision rather than an
// accident.
func TestExtractStatsSumsPerEntry(t *testing.T) {
	one := assistantWithUsage("answer", 2, 1000, 500, 40)
	one.Message.ID = "msg1"
	two := assistantWithUsage("answer", 2, 1000, 500, 40)
	two.Message.ID = "msg1"

	s := ExtractStats([]cc.Entry{one, two})
	if s.InputTokens != 4 || s.CacheReads != 2000 {
		t.Errorf("ExtractStats totals = in %d, reads %d; want 4, 2000 (per-entry sum)",
			s.InputTokens, s.CacheReads)
	}
	recs := ccusage.FromEntries("claude-code", []cc.Entry{one, two})
	if len(recs) != 1 || recs[0].Input != 2 || recs[0].CacheRead != 1000 {
		t.Errorf("FromEntries = %+v; want one record with in 2, read 1000", recs)
	}
}
