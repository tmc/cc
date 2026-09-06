package cc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tokenCount renders one Codex token_count event: the tokens of the request
// that just finished, beside the session-to-date running total that lets a
// re-sent event be told from a new request.
func tokenCount(ts string, in, cached, out, totalIn, totalCached, totalOut int) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{`+
		`"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"total_tokens":%d},`+
		`"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"total_tokens":%d}}}}`,
		ts, in, cached, out, in+out, totalIn, totalCached, totalOut, totalIn+totalOut)
}

const codexAssistant = `{"timestamp":"2026-02-25T10:00:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}}`

// usageOf returns the usage on the single assistant entry in entries.
func usageOf(t *testing.T, entries []Entry) *Usage {
	t.Helper()
	var found *Usage
	n := 0
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "assistant" {
			n++
			found = e.Message.Usage
		}
	}
	if n != 1 {
		t.Fatalf("got %d assistant entries, want 1", n)
	}
	if found == nil {
		t.Fatal("assistant entry carries no usage")
	}
	return found
}

// readBoth reads jsonl through both paths cc offers — the streaming Reader and
// ReadFile's whole-file read — and returns their entries. The two used to
// disagree about which of several token_count events won; they must now agree.
func readBoth(t *testing.T, jsonl string) (streamed, whole []Entry) {
	t.Helper()
	r := NewReader(context.Background(), strings.NewReader(jsonl))
	for r.Next() {
		streamed = append(streamed, r.Entry())
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Reader error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(path, []byte(jsonl), 0o600); err != nil {
		t.Fatal(err)
	}
	whole, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	return streamed, whole
}

// TestCodexTokenCountSums checks that several token_count events arriving
// between two assistant entries all land on the preceding one. Keeping only the
// first or only the last, which the two reader paths used to do respectively,
// drops tokens.
func TestCodexTokenCountSums(t *testing.T) {
	jsonl := codexAssistant + "\n" +
		tokenCount("2026-02-25T10:00:01Z", 100, 40, 10, 100, 40, 10) + "\n" +
		tokenCount("2026-02-25T10:00:02Z", 200, 60, 20, 300, 100, 30) + "\n"

	streamed, whole := readBoth(t, jsonl)
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			u := usageOf(t, tc.entries)
			if u.InputTokens != 300 || u.CacheReadInputTokens != 100 || u.OutputTokens != 30 {
				t.Errorf("usage = in %d, read %d, out %d; want 300, 100, 30",
					u.InputTokens, u.CacheReadInputTokens, u.OutputTokens)
			}
		})
	}
}

// TestCodexTokenCountRepeatIgnored checks that an event whose running total has
// not moved is not counted again. Codex re-sends the last counter when only its
// rate-limit block changed, and summing that would double one request.
func TestCodexTokenCountRepeatIgnored(t *testing.T) {
	jsonl := codexAssistant + "\n" +
		tokenCount("2026-02-25T10:00:01Z", 100, 40, 10, 100, 40, 10) + "\n" +
		tokenCount("2026-02-25T10:00:02Z", 100, 40, 10, 100, 40, 10) + "\n"

	streamed, whole := readBoth(t, jsonl)
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			u := usageOf(t, tc.entries)
			if u.InputTokens != 100 || u.OutputTokens != 10 {
				t.Errorf("usage = in %d, out %d; want 100, 10 (the repeat must not count)",
					u.InputTokens, u.OutputTokens)
			}
		})
	}
}

// TestCodexTokenCountAcrossEntries checks that events are attributed to the
// most recent assistant entry even with other entries in between, and that a
// second assistant entry starts a fresh attribution.
func TestCodexTokenCountAcrossEntries(t *testing.T) {
	const userTurn = `{"timestamp":"2026-02-25T10:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}}`
	const secondAssistant = `{"timestamp":"2026-02-25T10:00:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"again"}]}}`

	jsonl := codexAssistant + "\n" +
		tokenCount("2026-02-25T10:00:01Z", 100, 40, 10, 100, 40, 10) + "\n" +
		userTurn + "\n" +
		secondAssistant + "\n" +
		tokenCount("2026-02-25T10:00:05Z", 200, 60, 20, 300, 100, 30) + "\n"

	streamed, whole := readBoth(t, jsonl)
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			var got []*Usage
			for _, e := range tc.entries {
				if e.Message != nil && e.Message.Role == "assistant" {
					got = append(got, e.Message.Usage)
				}
			}
			if len(got) != 2 {
				t.Fatalf("got %d assistant entries, want 2", len(got))
			}
			if got[0] == nil || got[0].InputTokens != 100 || got[0].OutputTokens != 10 {
				t.Errorf("first assistant usage = %+v, want in 100, out 10", got[0])
			}
			if got[1] == nil || got[1].InputTokens != 200 || got[1].OutputTokens != 20 {
				t.Errorf("second assistant usage = %+v, want in 200, out 20", got[1])
			}
		})
	}
}

// TestCodexTokenCountBeforeAssistant checks that an event with no assistant
// entry before it keeps its tokens without being attributed to a later
// message, which would bill a request to the wrong one. Codex emits these for
// a sub-agent turn whose messages are not in the file, and a tail read starts
// with them; they are returned as their own entry, and ccusage counts them
// unattributed.
func TestCodexTokenCountBeforeAssistant(t *testing.T) {
	jsonl := tokenCount("2026-02-25T10:00:01Z", 100, 40, 10, 100, 40, 10) + "\n" + codexAssistant + "\n"

	streamed, whole := readBoth(t, jsonl)
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			var loose []*Usage
			for _, e := range tc.entries {
				if e.Message != nil && e.Message.Role == "assistant" && e.Message.Usage != nil {
					t.Errorf("assistant gained usage from an event that preceded it: %+v", e.Message.Usage)
				}
				if e.Type == "system" && e.Subtype == "token_count" && e.Usage != nil {
					loose = append(loose, e.Usage)
				}
			}
			if len(loose) != 1 {
				t.Fatalf("got %d unattributed token_count entries, want 1", len(loose))
			}
			if loose[0].InputTokens != 100 || loose[0].OutputTokens != 10 {
				t.Errorf("unattributed usage = in %d, out %d; want 100, 10",
					loose[0].InputTokens, loose[0].OutputTokens)
			}
		})
	}
}

// TestCodexCacheWrite checks that the tokens Codex reports writing to its cache
// reach Usage.CacheCreationInputTokens. They are a subset of input_tokens, as
// the event's own total_tokens (input+output) shows, which is why ccusage
// subtracts them; the reader carries the provider's numbers through unchanged.
func TestCodexCacheWrite(t *testing.T) {
	// A real event: 34143 prompt tokens of which 32976 were read from cache
	// and 1164 written to it, leaving 3 new.
	const event = `{"timestamp":"2026-02-25T10:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{` +
		`"last_token_usage":{"input_tokens":34143,"cached_input_tokens":32976,"cache_write_input_tokens":1164,"output_tokens":153,"total_tokens":34296},` +
		`"total_token_usage":{"input_tokens":34143,"cached_input_tokens":32976,"cache_write_input_tokens":1164,"output_tokens":153,"total_tokens":34296}}}}`

	streamed, whole := readBoth(t, codexAssistant+"\n"+event+"\n")
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			u := usageOf(t, tc.entries)
			if u.InputTokens != 34143 || u.CacheReadInputTokens != 32976 ||
				u.CacheCreationInputTokens != 1164 || u.OutputTokens != 153 {
				t.Errorf("usage = %+v; want in 34143, read 32976, create 1164, out 153", u)
			}
		})
	}
}

// TestCodexTurnContextModel checks that the model a turn_context announces is
// stamped onto the turn's assistant messages. It is the only model string a
// rollout carries, so without this every Codex request has an empty model and
// no vendor.
func TestCodexTurnContextModel(t *testing.T) {
	const turnContext = `{"timestamp":"2026-02-25T09:59:00Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/w","model":"gpt-5.4-codex"}}`
	const toolCall = `{"timestamp":"2026-02-25T10:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{}"}}`

	streamed, whole := readBoth(t, turnContext+"\n"+codexAssistant+"\n"+toolCall+"\n")
	for _, tc := range []struct {
		name    string
		entries []Entry
	}{{"streaming", streamed}, {"whole file", whole}} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			for _, e := range tc.entries {
				if e.Message == nil || e.Message.Role != "assistant" {
					continue
				}
				n++
				if e.Message.Model != "gpt-5.4-codex" {
					t.Errorf("assistant model = %q, want gpt-5.4-codex", e.Message.Model)
				}
			}
			if n != 2 {
				t.Errorf("got %d assistant entries, want 2", n)
			}
		})
	}
}
