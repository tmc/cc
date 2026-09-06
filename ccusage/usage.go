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
	SessionID string
	// RequestID identifies the API request across files: the harness's own
	// message id, which a forked or resumed session copies verbatim along
	// with the rest of the ancestor's transcript. It is empty when the
	// harness records no id for the request, as Codex's token_count entries
	// do not.
	RequestID   string
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
// Four normalizations happen here.
//
// Codex and Gemini report cached prompt tokens as a subset of their input
// count, and Codex reports the tokens it wrote to the cache as a second subset
// of it: its own total_tokens is input plus output, so neither cache count is
// an addend. For those agents Input is the provider's input minus both, floored
// at zero. Every other harness already reports the counts separately and is
// passed through.
//
// Assistant entries sharing a message id are one request: Claude Code writes
// one entry per streamed block of an API message, each repeating that message's
// prompt counts. They fold into a single Record with the largest count seen for
// each field, at the position of the first entry. Entries with no message id
// are one Record each.
//
// A usage-bearing token_count entry with no assistant message is a request the
// reader could not attribute — Codex emits the counter after the fact, and the
// message it describes can be missing from the file, as it is for a sub-agent
// turn. It becomes a Record of its own with an empty Model, so its tokens stay
// in the totals rather than being dropped.
//
// An output count of 1 or 0 is Claude Code's streaming-start snapshot, not a
// measurement: when the request has text, Output is estimated by tokenizing
// every entry folded into it and OutputEstimated is set. This is the estimate
// the cass collector has always applied; see [EstimateOutput].
// syntheticModel is the model Claude Code stamps on a message it wrote
// itself rather than received from a provider.
const syntheticModel = "<synthetic>"

func FromEntries(agent string, entries []cc.Entry) []Record {
	cacheInInput := cacheInInput(agent)
	var (
		recs []Record
		msgs [][]*cc.Message // the messages folded into each record, for the estimate
		byID = make(map[string]int)
	)
	for _, e := range entries {
		m, u := usageOf(e)
		if u == nil {
			continue
		}
		r := Record{
			SessionID:   e.SessionID,
			Agent:       agent,
			Time:        e.Timestamp.UTC(),
			CWD:         e.CWD,
			IsSidechain: e.IsSidechain,
			Input:       u.InputTokens,
			CacheRead:   u.CacheReadInputTokens,
			CacheCreate: u.CacheCreationInputTokens,
			Output:      u.OutputTokens,
		}
		if m != nil {
			if m.Model == syntheticModel {
				// Claude Code writes an assistant message of its own when it
				// has something to say without asking the model -- a rate
				// limit notice, an API error. Its usage is all zeros and its
				// text is not a completion, so it is not a request.
				continue
			}
			r.Model, r.Vendor = m.Model, Vendor(m.Model)
			r.RequestID = m.ID
		}
		if cacheInInput {
			r.Input = max(0, r.Input-r.CacheRead-r.CacheCreate)
		}
		if m != nil {
			if i, ok := byID[m.ID]; ok {
				recs[i].Input = max(recs[i].Input, r.Input)
				recs[i].CacheRead = max(recs[i].CacheRead, r.CacheRead)
				recs[i].CacheCreate = max(recs[i].CacheCreate, r.CacheCreate)
				recs[i].Output = max(recs[i].Output, r.Output)
				if recs[i].Model == "" {
					recs[i].Model, recs[i].Vendor = r.Model, r.Vendor
				}
				msgs[i] = append(msgs[i], m)
				continue
			}
			if m.ID != "" {
				byID[m.ID] = len(recs)
			}
			recs, msgs = append(recs, r), append(msgs, []*cc.Message{m})
			continue
		}
		recs, msgs = append(recs, r), append(msgs, nil)
	}
	for i := range recs {
		if out, estimated := estimate(recs[i].Output, foldedText(msgs[i])); estimated {
			recs[i].Output, recs[i].OutputEstimated = out, true
		}
	}
	return recs
}

// usageOf returns the usage one entry reports and the assistant message it
// belongs to, or a nil usage when the entry is not a request. The message is
// nil for a token_count entry the reader could not attach to an assistant
// entry; see [FromEntries].
func usageOf(e cc.Entry) (*cc.Message, *cc.Usage) {
	if m := e.Message; m != nil {
		if m.Role == "assistant" && m.Usage != nil {
			return m, m.Usage
		}
		return nil, nil
	}
	if e.Type == "system" && e.Subtype == "token_count" {
		return nil, e.Usage
	}
	return nil, nil
}

// foldedText joins the text of every message folded into one record. Claude
// Code splits a message across entries a block at a time, so tokenizing only
// one of them would miss the rest of the same request's output.
func foldedText(msgs []*cc.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		text := m.TextContent()
		if text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	return b.String()
}

// cacheInInput reports whether agent's usage counts cached prompt tokens
// inside its input count. Codex ("codex-cli", "codex-app") and Gemini
// ("gemini-cli") do; Claude, opencode and pi report them separately, which
// their session files prove: opencode records cache reads larger than input,
// and pi does the same for every model it caches with.
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
// message whose provider reported the given count, and whether the count was
// estimated. A reported count above 1 is returned unchanged. A count of 1 or 0
// is a Claude Code entry written before the completion finished, carrying the
// streaming-start snapshot — the final count lives only in the SSE
// message_delta, which the JSONL never sees — so the message text is tokenized
// instead. The reported count stands when the message has no text or the
// tokenizer is unavailable.
//
// [FromEntries] estimates over every entry of a folded request rather than one
// message; a caller holding a single message uses this.
func EstimateOutput(reported int, msg *cc.Message) (int, bool) {
	if msg == nil {
		return reported, false
	}
	return estimate(reported, msg.TextContent())
}

// estimate implements [EstimateOutput] over already-extracted text.
func estimate(reported int, text string) (int, bool) {
	if reported > 1 || text == "" {
		return reported, false
	}
	tc := counter()
	if tc == nil {
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
