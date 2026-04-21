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

func TestContentBlock_BashCommand(t *testing.T) {
	tests := []struct {
		name string
		b    ContentBlock
		want string
	}{
		{
			name: "bash tool_use",
			b:    ContentBlock{Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"ls -la"}`)},
			want: "ls -la",
		},
		{
			name: "non-bash tool_use",
			b:    ContentBlock{Type: "tool_use", Name: "Read", Input: json.RawMessage(`{"file_path":"/tmp/x"}`)},
			want: "",
		},
		{
			name: "not tool_use",
			b:    ContentBlock{Type: "text", Text: "hello"},
			want: "",
		},
		{
			name: "empty input",
			b:    ContentBlock{Type: "tool_use", Name: "Bash"},
			want: "",
		},
		{
			name: "bash with description",
			b:    ContentBlock{Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{"command":"go test ./...","description":"run tests"}`)},
			want: "go test ./...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.b.BashCommand()
			if got != tt.want {
				t.Fatalf("BashCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}
