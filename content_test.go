package cc

import (
	"encoding/json"
	"testing"
)

func TestExtractAnyText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "plain string",
			raw:  `"hello world"`,
			want: "hello world",
		},
		{
			name: "content blocks",
			raw:  `[{"type":"text","text":"first"},{"type":"text","text":"second"}]`,
			want: "first\nsecond",
		},
		{
			name: "gemini array text",
			raw:  `[{"text":"gemini prompt"}]`,
			want: "gemini prompt",
		},
		{
			name: "nested parts",
			raw:  `{"parts":[{"text":"nested text"}]}`,
			want: "nested text",
		},
		{
			name: "empty",
			raw:  `{}`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractAnyText(json.RawMessage(tt.raw))
			if got != tt.want {
				t.Fatalf("ExtractAnyText(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
