package ccusage

import (
	"strings"
	"sync"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/tokencount/anthropictokenizer"
)

// Record is one model request with normalized token accounting. Input,
// CacheRead, CacheCreate and Output are disjoint: the prompt the model read is
// Input+CacheRead+CacheCreate, and the whole request is that plus Output.
type Record struct {
	SessionID   string
	Agent       string    // harness slug as cass names it
	Time        time.Time // request timestamp, UTC
	Model       string
	Vendor      string // derived from Model
	CWD         string // per-entry cwd when the harness records one, else ""
	IsSidechain bool

	Input, CacheRead, CacheCreate, Output int
	OutputEstimated                       bool // Output came from BPE, not the provider
}

// Context returns the prompt tokens the model read.
func (r Record) Context() int { return r.Input + r.CacheRead + r.CacheCreate }

// Total returns the context plus the completion.
func (r Record) Total() int { return r.Context() + r.Output }

// FromEntries returns one Record per model request in entries, normalized to
// the Anthropic convention for agent. It does not resolve git context.
//
// Three normalizations happen here.
//
// Codex and Gemini report cached prompt tokens as a subset of their input
// count, so for those agents Input is the provider's input minus its cached
// count, floored at zero. Every other harness already reports the two
// separately and is passed through.
//
// Assistant entries sharing a message id are one request: Claude Code writes
// one entry per streamed block, each repeating the prompt counts and carrying
// a larger output snapshot. They fold into a single Record with the largest
// count seen for each field, at the position of the first entry. Entries with
// no message id are one Record each.
//
// An output count of 1 or 0 is Claude Code's streaming-start snapshot, not a
// measurement: when the message has text, Output is estimated by tokenizing it
// and OutputEstimated is set. This is the estimate the cass collector has
// always applied; see [EstimateOutput].
func FromEntries(agent string, entries []cc.Entry) []Record {
	cacheInInput := cacheInInput(agent)
	var (
		recs []Record
		msgs []*cc.Message // longest-output message per record, for the estimate
		byID = make(map[string]int)
	)
	for _, e := range entries {
		m := e.Message
		if m == nil || m.Role != "assistant" || m.Usage == nil {
			continue
		}
		r := Record{
			SessionID:   e.SessionID,
			Agent:       agent,
			Time:        e.Timestamp.UTC(),
			Model:       m.Model,
			Vendor:      Vendor(m.Model),
			CWD:         e.CWD,
			IsSidechain: e.IsSidechain,
			Input:       m.Usage.InputTokens,
			CacheRead:   m.Usage.CacheReadInputTokens,
			CacheCreate: m.Usage.CacheCreationInputTokens,
			Output:      m.Usage.OutputTokens,
		}
		if cacheInInput {
			r.Input = max(0, r.Input-r.CacheRead)
		}
		if i, ok := byID[m.ID]; ok {
			if r.Output > recs[i].Output {
				msgs[i] = m
			}
			recs[i].Input = max(recs[i].Input, r.Input)
			recs[i].CacheRead = max(recs[i].CacheRead, r.CacheRead)
			recs[i].CacheCreate = max(recs[i].CacheCreate, r.CacheCreate)
			recs[i].Output = max(recs[i].Output, r.Output)
			if recs[i].Model == "" {
				recs[i].Model, recs[i].Vendor = r.Model, r.Vendor
			}
			continue
		}
		if m.ID != "" {
			byID[m.ID] = len(recs)
		}
		recs = append(recs, r)
		msgs = append(msgs, m)
	}
	for i := range recs {
		if out, estimated := EstimateOutput(recs[i].Output, msgs[i]); estimated {
			recs[i].Output, recs[i].OutputEstimated = out, true
		}
	}
	return recs
}

// cacheInInput reports whether agent's usage counts cached prompt tokens
// inside its input count. Codex ("codex-cli", "codex-app") and Gemini
// ("gemini-cli") do; Claude, opencode and pi report the two separately, which
// their session files prove: opencode records cache reads larger than input,
// and pi's own totalTokens is the sum of all four.
func cacheInInput(agent string) bool {
	return strings.HasPrefix(agent, "codex") || strings.HasPrefix(agent, "gemini")
}

// Vendor returns the vendor of a model string: "anthropic", "openai",
// "google", or "other". It returns "" for an empty model, which distinguishes
// a request that recorded no model from one whose vendor is unrecognized.
//
// The agent a session ran under does not determine its vendor: pi and gemini
// sessions on disk drive claude-* models, and opencode routes every vendor
// through openrouter with a "vendor/model" model id, which is why the prefix
// is matched before the substrings.
func Vendor(model string) string {
	m := strings.ToLower(model)
	if m == "" {
		return ""
	}
	if i := strings.Index(m, "/"); i > 0 {
		switch m[:i] {
		case "anthropic":
			return "anthropic"
		case "openai":
			return "openai"
		case "google", "google-vertex", "googleai":
			return "google"
		}
	}
	switch {
	case strings.Contains(m, "claude"), strings.Contains(m, "opus"),
		strings.Contains(m, "sonnet"), strings.Contains(m, "haiku"),
		strings.Contains(m, "fable"):
		return "anthropic"
	case strings.Contains(m, "gpt"), strings.Contains(m, "codex"):
		return "openai"
	case strings.Contains(m, "gemini"):
		return "google"
	}
	return "other"
}

// EstimateOutput returns the output token count to record for an assistant
// message whose provider reported reported tokens, and whether the count was
// estimated. A reported count above 1 is returned unchanged. A count of 1 or 0
// is Claude Code's streaming-start snapshot — the final count lives only in the
// SSE message_delta, which the JSONL never sees — so the message text is
// tokenized instead. The reported count stands when the message has no text or
// the tokenizer is unavailable.
func EstimateOutput(reported int, msg *cc.Message) (int, bool) {
	if reported > 1 || msg == nil {
		return reported, false
	}
	tc := counter()
	if tc == nil {
		return reported, false
	}
	text := msg.TextContent()
	if text == "" {
		return reported, false
	}
	return tc.Count(text), true
}

// counter returns the shared token counter, or nil if it could not be built.
// Estimation is a fallback, so a counter that fails to initialize degrades to
// reporting the provider's number rather than failing the read.
var counter = sync.OnceValue(func() *anthropictokenizer.Counter {
	c, err := anthropictokenizer.NewCounter()
	if err != nil {
		return nil
	}
	return c
})
