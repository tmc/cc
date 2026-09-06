package ccusage

import (
	"testing"
	"time"

	"github.com/tmc/cc"
)

// entry builds an assistant entry carrying usage, as every harness's reader
// produces one.
func entry(id, model string, in, cacheRead, cacheCreate, out int) cc.Entry {
	return cc.Entry{
		SessionID: "s1",
		Timestamp: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
		Message: &cc.Message{
			ID:    id,
			Role:  "assistant",
			Model: model,
			Usage: &cc.Usage{
				InputTokens:              in,
				OutputTokens:             out,
				CacheReadInputTokens:     cacheRead,
				CacheCreationInputTokens: cacheCreate,
			},
		},
	}
}

// TestFromEntriesPerHarness pins the normalization for one request from each
// harness, with counts taken from real session files: codex and gemini report
// cached tokens inside their input count and must lose them, the rest are
// already disjoint and must be passed through untouched.
func TestFromEntriesPerHarness(t *testing.T) {
	tests := []struct {
		name                            string
		agent                           string
		in, cacheRead, cacheCreate, out int
		wantIn, wantRead, wantCreate    int
		wantOut                         int
		model, wantVendor               string
	}{
		{
			// A real ~/.claude assistant line: 2 uncached tokens beside a
			// 21263-token cache read, already the Anthropic convention.
			name: "claude", agent: "claude-code", model: "claude-opus-5",
			in: 2, cacheRead: 21263, cacheCreate: 11913, out: 394,
			wantIn: 2, wantRead: 21263, wantCreate: 11913, wantOut: 394,
			wantVendor: "anthropic",
		},
		{
			// A real codex token_count: cached_input_tokens is a subset of
			// input_tokens, so 26532-24832 = 1700 tokens were actually new.
			name: "codex", agent: "codex-cli", model: "gpt-5.4-codex",
			in: 26532, cacheRead: 24832, cacheCreate: 0, out: 280,
			wantIn: 1700, wantRead: 24832, wantCreate: 0, wantOut: 280,
			wantVendor: "openai",
		},
		{
			name: "codex-app", agent: "codex-app", model: "gpt-5.4",
			in: 100, cacheRead: 90, cacheCreate: 0, out: 5,
			wantIn: 10, wantRead: 90, wantCreate: 0, wantOut: 5,
			wantVendor: "openai",
		},
		{
			// A real gemini chat message: cached 8201 of 9936 prompt tokens.
			name: "gemini", agent: "gemini-cli", model: "gemini-3-pro",
			in: 9936, cacheRead: 8201, cacheCreate: 0, out: 100,
			wantIn: 1735, wantRead: 8201, wantCreate: 0, wantOut: 100,
			wantVendor: "google",
		},
		{
			// A real opencode record: the cache read dwarfs input, which is
			// only possible because input excludes it.
			name: "opencode", agent: "opencode", model: "anthropic/claude-opus-4.6",
			in: 658, cacheRead: 85312, cacheCreate: 0, out: 316,
			wantIn: 658, wantRead: 85312, wantCreate: 0, wantOut: 316,
			wantVendor: "anthropic",
		},
		{
			// A real pi record, whose own totalTokens 13396 is the sum of all
			// four counts, proving they are disjoint.
			name: "pi", agent: "pi", model: "claude-opus-4-6",
			in: 1, cacheRead: 13288, cacheCreate: 81, out: 26,
			wantIn: 1, wantRead: 13288, wantCreate: 81, wantOut: 26,
			wantVendor: "anthropic",
		},
		{
			// Cached exceeding input cannot happen upstream, but a floor is
			// cheaper than a negative token count leaking into a sum.
			name: "codex cached over input", agent: "codex-cli", model: "gpt-5.4",
			in: 10, cacheRead: 40, cacheCreate: 0, out: 3,
			wantIn: 0, wantRead: 40, wantCreate: 0, wantOut: 3,
			wantVendor: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recs := FromEntries(tt.agent, []cc.Entry{
				entry("msg1", tt.model, tt.in, tt.cacheRead, tt.cacheCreate, tt.out),
			})
			if len(recs) != 1 {
				t.Fatalf("FromEntries returned %d records, want 1", len(recs))
			}
			r := recs[0]
			if r.Input != tt.wantIn || r.CacheRead != tt.wantRead ||
				r.CacheCreate != tt.wantCreate || r.Output != tt.wantOut {
				t.Errorf("counts = in %d, read %d, create %d, out %d; want %d, %d, %d, %d",
					r.Input, r.CacheRead, r.CacheCreate, r.Output,
					tt.wantIn, tt.wantRead, tt.wantCreate, tt.wantOut)
			}
			if r.Vendor != tt.wantVendor {
				t.Errorf("Vendor = %q, want %q", r.Vendor, tt.wantVendor)
			}
			if r.Agent != tt.agent {
				t.Errorf("Agent = %q, want %q", r.Agent, tt.agent)
			}
			if got, want := r.Context(), tt.wantIn+tt.wantRead+tt.wantCreate; got != want {
				t.Errorf("Context() = %d, want %d", got, want)
			}
			if got, want := r.Total(), tt.wantIn+tt.wantRead+tt.wantCreate+tt.wantOut; got != want {
				t.Errorf("Total() = %d, want %d", got, want)
			}
		})
	}
}

// TestFromEntriesFoldsMessageID checks that the several entries Claude Code
// writes for one streamed API message become one request. Summing them instead
// would roughly double the prompt counts, since every entry repeats them.
func TestFromEntriesFoldsMessageID(t *testing.T) {
	entries := []cc.Entry{
		entry("msg1", "claude-opus-5", 2, 21263, 11913, 1),
		entry("msg1", "claude-opus-5", 2, 21263, 11913, 200),
		entry("msg1", "claude-opus-5", 2, 21263, 11913, 394),
		entry("msg2", "claude-opus-5", 5, 33000, 0, 282),
	}
	recs := FromEntries("claude-code", entries)
	if len(recs) != 2 {
		t.Fatalf("FromEntries returned %d records, want 2", len(recs))
	}
	if recs[0].Input != 2 || recs[0].CacheRead != 21263 || recs[0].CacheCreate != 11913 {
		t.Errorf("folded prompt counts = %d/%d/%d, want 2/21263/11913",
			recs[0].Input, recs[0].CacheRead, recs[0].CacheCreate)
	}
	if recs[0].Output != 394 {
		t.Errorf("folded output = %d, want 394 (the last snapshot)", recs[0].Output)
	}
	if recs[1].Output != 282 {
		t.Errorf("second record output = %d, want 282", recs[1].Output)
	}
}

// TestFromEntriesSkipsNonRequests checks the entries that carry no request:
// user turns, assistant messages with no usage, and a nil message.
func TestFromEntriesSkipsNonRequests(t *testing.T) {
	entries := []cc.Entry{
		{Type: "user", Message: &cc.Message{Role: "user"}},
		{Type: "assistant", Message: &cc.Message{Role: "assistant"}},
		{Type: "summary"},
		entry("msg1", "claude-opus-5", 7, 0, 0, 9),
	}
	recs := FromEntries("claude-code", entries)
	if len(recs) != 1 {
		t.Fatalf("FromEntries returned %d records, want 1", len(recs))
	}
	if recs[0].Input != 7 || recs[0].Output != 9 {
		t.Errorf("record = in %d, out %d; want 7, 9", recs[0].Input, recs[0].Output)
	}
}

// TestFromEntriesCarriesEntryFields checks the fields a Record takes from the
// entry rather than from the usage: the sidechain tag decides whether a
// request can be filtered out of a total, and the timestamp is what every
// time-bucketed view groups by.
func TestFromEntriesCarriesEntryFields(t *testing.T) {
	e := entry("msg1", "claude-opus-5", 1, 2, 3, 4)
	e.IsSidechain = true
	e.CWD = "/Volumes/example/go/src/github.com/tmc/cc"
	e.Timestamp = time.Date(2026, 9, 5, 19, 30, 0, 0, time.FixedZone("PDT", -7*3600))

	recs := FromEntries("claude-code", []cc.Entry{e})
	if len(recs) != 1 {
		t.Fatalf("FromEntries returned %d records, want 1", len(recs))
	}
	r := recs[0]
	if !r.IsSidechain {
		t.Error("IsSidechain = false, want true")
	}
	if r.CWD != e.CWD {
		t.Errorf("CWD = %q, want %q", r.CWD, e.CWD)
	}
	if r.Time.Location() != time.UTC {
		t.Errorf("Time zone = %v, want UTC", r.Time.Location())
	}
	if want := time.Date(2026, 9, 6, 2, 30, 0, 0, time.UTC); !r.Time.Equal(want) {
		t.Errorf("Time = %v, want %v", r.Time, want)
	}
	if r.SessionID != "s1" {
		t.Errorf("SessionID = %q, want %q", r.SessionID, "s1")
	}
}

func TestVendor(t *testing.T) {
	tests := []struct{ model, want string }{
		{"", ""},
		{"claude-opus-5", "anthropic"},
		{"claude-sonnet-4-5-20250929", "anthropic"},
		{"claude-fable-5-1", "anthropic"},
		{"gpt-5.4-codex", "openai"},
		{"gemini-3-pro", "google"},
		{"anthropic/claude-opus-4.6", "anthropic"},
		{"openai/gpt-5.4", "openai"},
		{"google/gemini-3-pro", "google"},
		// Routed through openrouter, so the prefix decides and neither
		// substring rule fires.
		{"moonshotai/kimi-k2.6", "other"},
		{"deepseek/deepseek-v4-flash", "other"},
		{"qwen/qwen3-32b", "other"},
	}
	for _, tt := range tests {
		if got := Vendor(tt.model); got != tt.want {
			t.Errorf("Vendor(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestWeight(t *testing.T) {
	r := Record{Input: 10, CacheRead: 100, CacheCreate: 20, Output: 5}
	p := Pricing{Input: 1, Output: 2, CacheRead: 0.1, CacheCreate: 0.5}
	tests := []struct {
		unit string
		want float64
	}{
		{"requests", 1},
		{"input", 10},
		{"output", 5},
		{"context", 130},
		{"total", 135},
		{"tokens", 135},
		{"TOTAL", 135},
		// 10*1 + 5*2 + 100*0.1 + 20*0.5 = 40.
		{"cost", 40},
		{"nonsense", 40},
	}
	for _, tt := range tests {
		if got := Weight(r, tt.unit, p); got != tt.want {
			t.Errorf("Weight(unit %q) = %v, want %v", tt.unit, got, tt.want)
		}
	}
}

func TestPricingForModel(t *testing.T) {
	custom := Pricing{Input: 9, Output: 9, CacheRead: 9, CacheCreate: 9}
	cfg := PricingConfig{Models: map[string]Pricing{"claude-opus-5": custom}}

	if got := PricingForModel("claude-opus-5", cfg); got != custom {
		t.Errorf("exact model = %v, want %v", got, custom)
	}
	if got := PricingForModel("claude-opus-4-6", cfg); got.Input != 1.5 {
		t.Errorf("opus family Input = %v, want 1.5", got.Input)
	}
	if got := PricingForModel("claude-haiku-4-5", cfg); got.Input != 0.3 {
		t.Errorf("haiku family Input = %v, want 0.3", got.Input)
	}
	if got := PricingForModel("gpt-5.4", cfg); got != DefaultPricing {
		t.Errorf("unknown model = %v, want %v", got, DefaultPricing)
	}

	// A family key in the config beats the built-in family rates.
	fam := PricingConfig{Models: map[string]Pricing{"opus": custom}}
	if got := PricingForModel("claude-opus-4-6", fam); got != custom {
		t.Errorf("family key = %v, want %v", got, custom)
	}

	// Default applies to everything no key matches, including a family that
	// has built-in rates.
	def := Pricing{Input: 7}
	withDefault := PricingConfig{Default: &def}
	if got := PricingForModel("claude-opus-4-6", withDefault); got != def {
		t.Errorf("config default = %v, want %v", got, def)
	}
}

func TestModelFamily(t *testing.T) {
	tests := []struct{ model, want string }{
		{"", "unknown"},
		{"claude-opus-5", "opus"},
		{"claude-haiku-4-5", "haiku"},
		{"claude-sonnet-4-5", "sonnet"},
		{"GPT-5.4", "gpt-5.4"},
	}
	for _, tt := range tests {
		if got := ModelFamily(tt.model); got != tt.want {
			t.Errorf("ModelFamily(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}

func TestLoadPricing(t *testing.T) {
	cfg, err := LoadPricing("")
	if err != nil {
		t.Fatalf("LoadPricing(\"\") error = %v", err)
	}
	if cfg.Models == nil {
		t.Error("LoadPricing(\"\") left Models nil, want an empty map")
	}
	if _, err := LoadPricing("/nonexistent/pricing.json"); err == nil {
		t.Error("LoadPricing of a missing file succeeded, want an error")
	}
}
