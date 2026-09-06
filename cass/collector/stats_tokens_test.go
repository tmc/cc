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
// estimate moved into ccusage, kept as an independent check on the move. It
// agrees with ExtractStats on any session whose requests are one entry each,
// which is every harness but Claude Code; where a request spans several
// entries the two deliberately differ, and TestExtractStatsFoldsMessageID
// covers that.
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

// TestExtractStatsTokenTotals pins the five token fields ExtractStats reports,
// on sessions whose requests are one entry each. They are persisted in
// stats_json, so a change in this arithmetic disagrees with every session
// indexed before it.
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

// TestExtractStatsFoldsMessageID checks that the several entries Claude Code
// writes for one streamed API message are counted once. Summing them, which
// ExtractStats used to do, roughly doubled the input and cache-read totals of
// every Claude session in the index.
func TestExtractStatsFoldsMessageID(t *testing.T) {
	one := assistantWithUsage("answer", 2, 1000, 500, 1)
	one.Message.ID = "msg1"
	two := assistantWithUsage("answer", 2, 1000, 500, 40)
	two.Message.ID = "msg1"
	other := assistantWithUsage("second", 7, 300, 0, 20)
	other.Message.ID = "msg2"

	s := ExtractStats([]cc.Entry{one, two, other})
	if s.InputTokens != 9 {
		t.Errorf("InputTokens = %d, want 9 (2 folded + 7)", s.InputTokens)
	}
	if s.CacheReads != 1300 {
		t.Errorf("CacheReads = %d, want 1300 (1000 folded + 300)", s.CacheReads)
	}
	if s.CacheCreationInputTokens != 500 {
		t.Errorf("CacheCreationInputTokens = %d, want 500", s.CacheCreationInputTokens)
	}
	if s.OutputTokensSnapshot != 60 {
		t.Errorf("OutputTokensSnapshot = %d, want 60 (40 folded + 20)", s.OutputTokensSnapshot)
	}
	if s.OutputTokensEstimated {
		t.Error("OutputTokensEstimated = true, want false: the folded message ended above the snapshot")
	}
}

// TestExtractStatsAgreesWithFromEntries checks that the collector's totals are
// the sum of the records ccusage produces, so the per-session totals in
// stats_json and the per-request rows in usage_events cannot drift apart.
func TestExtractStatsAgreesWithFromEntries(t *testing.T) {
	entries := []cc.Entry{
		assistantWithUsage("first answer", 10, 100, 20, 1),
		assistantWithUsage("second answer", 5, 200, 0, 30),
		{Type: "user", Message: &cc.Message{Role: "user"}},
	}
	entries[0].Message.ID = "msg1"
	entries[1].Message.ID = "msg2"

	s := ExtractStats(entries)
	var in, out, cacheRead, cacheCreate int
	for _, r := range ccusage.FromEntries("", entries) {
		in, out = in+r.Input, out+r.Output
		cacheRead, cacheCreate = cacheRead+r.CacheRead, cacheCreate+r.CacheCreate
	}
	if s.InputTokens != in || s.OutputTokensSnapshot != out ||
		s.CacheReads != cacheRead || s.CacheCreationInputTokens != cacheCreate {
		t.Errorf("ExtractStats = in %d, out %d, read %d, create %d; FromEntries sums to %d, %d, %d, %d",
			s.InputTokens, s.OutputTokensSnapshot, s.CacheReads, s.CacheCreationInputTokens,
			in, out, cacheRead, cacheCreate)
	}
}

// TestExtractStatsCountsUnattributedUsage checks that a token_count entry the
// reader could not attach to an assistant message still reaches the totals.
// Codex emits the counter after the fact, and the message it describes is
// missing from a sub-agent turn or a tail read; dropping such an entry lost
// whole runs of tokens.
func TestExtractStatsCountsUnattributedUsage(t *testing.T) {
	entries := []cc.Entry{
		{
			Type:    "system",
			Subtype: "token_count",
			Usage:   &cc.Usage{InputTokens: 900, OutputTokens: 40, CacheReadInputTokens: 100},
		},
		assistantWithUsage("answer", 5, 200, 0, 30),
	}

	s := ExtractStats(entries)
	if s.InputTokens != 905 {
		t.Errorf("InputTokens = %d, want 905 (900 unattributed + 5)", s.InputTokens)
	}
	if s.CacheReads != 300 {
		t.Errorf("CacheReads = %d, want 300 (100 unattributed + 200)", s.CacheReads)
	}
	if s.OutputTokensSnapshot != 70 {
		t.Errorf("OutputTokensSnapshot = %d, want 70 (40 unattributed + 30)", s.OutputTokensSnapshot)
	}
}
