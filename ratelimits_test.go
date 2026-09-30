package cc

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRateLimitSamples(t *testing.T) {
	for _, tt := range []struct {
		name       string
		usage      string
		assistant  bool
		wantTokens int
	}{
		{"refresh", `null`, false, 0},
		{"zero", `{"last_token_usage":{"input_tokens":0}}`, false, 0},
		{"unattributed", `{"last_token_usage":{"input_tokens":10,"output_tokens":2}}`, false, 10},
		{"folded", `{"last_token_usage":{"input_tokens":10,"output_tokens":2},"total_token_usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`, true, 10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"type":"event_msg","timestamp":"2026-09-29T12:00:00Z","payload":{"type":"token_count","info":` + tt.usage + `,"rate_limits":{"primary":{"used_percent":25.5,"window_minutes":300,"resets_at":1790000000},"secondary":{"used_percent":0,"window_minutes":10080,"resets_at":1790000001}}}}` + "\n"
			if tt.assistant {
				line += line // A refresh repeats the cumulative token counter.
				line = `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}` + "\n" + line
			}
			for _, reader := range []struct {
				name string
				read func() ([]Entry, error)
			}{
				{"stream", func() ([]Entry, error) { return ReadAll(context.Background(), strings.NewReader(line)) }},
				{"offset", func() ([]Entry, error) {
					e, _, _, err := readComplete(context.Background(), strings.NewReader(line))
					return e, err
				}},
			} {
				t.Run(reader.name, func(t *testing.T) {
					entries, err := reader.read()
					if err != nil {
						t.Fatal(err)
					}
					sample := NewestRateLimits(entries)
					if sample == nil || sample.Primary == nil || sample.Primary.UsedPercent != 25.5 || sample.Primary.WindowMinutes != 300 || sample.Primary.ResetsAt != 1790000000 || sample.Secondary == nil || sample.Secondary.UsedPercent != 0 {
						t.Fatalf("sample = %+v", sample)
					}
					tokens := 0
					for _, e := range entries {
						if e.Usage != nil {
							tokens += e.Usage.InputTokens
						}
						if e.Message != nil && e.Message.Usage != nil {
							tokens += e.Message.Usage.InputTokens
						}
					}
					if tokens != tt.wantTokens {
						t.Fatalf("tokens = %d, want %d", tokens, tt.wantTokens)
					}
				})
			}
		})
	}
}

func TestNewestRateLimits(t *testing.T) {
	a, b := &RateLimits{Primary: &RateLimit{UsedPercent: 1}}, &RateLimits{Primary: &RateLimit{UsedPercent: 2}}
	now := time.Unix(100, 0)
	for _, tt := range []struct {
		name    string
		entries []Entry
		want    *RateLimits
	}{
		{"none", nil, nil},
		{"unsorted", []Entry{{Timestamp: now, RateLimits: a}, {Timestamp: now.Add(-time.Second), RateLimits: b}}, a},
		{"equal", []Entry{{Timestamp: now, RateLimits: a}, {Timestamp: now, RateLimits: b}}, b},
		{"undated", []Entry{{RateLimits: a}, {RateLimits: b}}, b},
		{"dated before undated", []Entry{{Timestamp: now, RateLimits: a}, {RateLimits: b}}, a},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewestRateLimits(tt.entries); got != tt.want {
				t.Fatalf("sample = %p, want %p", got, tt.want)
			}
		})
	}
}
