package ccdistill

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestRedactPatterns is a table over individual strings: each row asserts that
// a secret is replaced with its fixed token, and that ordinary code survives
// untouched. It drives the redaction engine through Redact on a single-block
// Sample so it exercises the same path the pipeline uses.
func TestRedactPatterns(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSubstr string // must appear in output ("" => no specific token)
		wantGone   string // must NOT appear in output ("" => skip check)
		wantSubs   int    // expected substitution count (-1 => don't assert)
	}{
		{
			name:       "openai sk key",
			in:         "key=sk-ABCDEFGHIJKLMNOPQRSTUVWX",
			wantSubstr: "[REDACTED_API_KEY]",
			wantGone:   "sk-ABCDEFGHIJKLMNOPQRSTUVWX",
			wantSubs:   1,
		},
		{
			name:       "anthropic sk-ant key",
			in:         "ANTHROPIC_API_KEY=sk-ant-api03-AAAAAAAAAAAAAAAAAAAAAA",
			wantSubstr: "[REDACTED_API_KEY]",
			wantGone:   "sk-ant-api03",
			wantSubs:   1,
		},
		{
			name:       "aws access key",
			in:         "id = AKIAIOSFODNN7EXAMPLE",
			wantSubstr: "[REDACTED_AWS_KEY]",
			wantGone:   "AKIAIOSFODNN7EXAMPLE",
			wantSubs:   1,
		},
		{
			name:       "github token",
			in:         "token ghp_" + strings.Repeat("a", 36),
			wantSubstr: "[REDACTED_GITHUB_TOKEN]",
			wantGone:   "ghp_aaaa",
			wantSubs:   1,
		},
		{
			name:       "email address",
			in:         "contact alice@example.com please",
			wantSubstr: "[REDACTED_EMAIL]",
			wantGone:   "alice@example.com",
			wantSubs:   1,
		},
		{
			// The rule scrubs the single token following the keyword. With a
			// bare "Authorization: <value>" the value is the next token and is
			// redacted while the keyword is preserved.
			name:       "authorization header keeps keyword scrubs value",
			in:         "Authorization: abc123secretvalue",
			wantSubstr: "Authorization [REDACTED_TOKEN]",
			wantGone:   "abc123secretvalue",
			wantSubs:   1,
		},
		{
			// "Bearer <value>" matches the bearer keyword; the value token is
			// scrubbed and the keyword preserved.
			name:       "bearer keyword keeps keyword scrubs value",
			in:         "Bearer abc123secretvalue",
			wantSubstr: "Bearer [REDACTED_TOKEN]",
			wantGone:   "abc123secretvalue",
			wantSubs:   1,
		},
		{
			name:       "private key block",
			in:         "-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAK\n-----END RSA PRIVATE KEY-----",
			wantSubstr: "[REDACTED_PRIVATE_KEY]",
			wantGone:   "MIIBOgIBAAJBAK",
			wantSubs:   1,
		},
		{
			name:       "plain code line is untouched",
			in:         "func add(a, b int) int { return a + b }",
			wantSubstr: "func add(a, b int) int { return a + b }",
			wantGone:   "REDACTED",
			wantSubs:   0,
		},
		{
			name:       "uuid and git sha preserved",
			in:         "session 0c3f1a2b-4d5e-6f70-8901-23456789abcd at commit deadbeefcafe",
			wantSubstr: "0c3f1a2b-4d5e-6f70-8901-23456789abcd",
			wantGone:   "REDACTED",
			wantSubs:   0,
		},
		{
			name:       "base64 blob without a key prefix is untouched",
			in:         "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
			wantSubstr: "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB",
			wantGone:   "REDACTED",
			wantSubs:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Sample{Turns: []Turn{
				{Role: RoleUser, Blocks: []Block{{Kind: KindText, Text: tt.in}}},
			}}
			subs := Redact(s)
			got := s.Turns[0].Blocks[0].Text

			if tt.wantSubstr != "" && !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("output %q missing %q", got, tt.wantSubstr)
			}
			if tt.wantGone != "" && strings.Contains(got, tt.wantGone) {
				t.Errorf("output %q still contains secret %q", got, tt.wantGone)
			}
			if tt.wantSubs >= 0 && subs != tt.wantSubs {
				t.Errorf("substitution count = %d, want %d (output %q)", subs, tt.wantSubs, got)
			}
		})
	}
}

// TestRedactHomePath verifies the absolute home-directory prefix is rewritten
// to "~" in both Block text and Meta paths. It uses the real os.UserHomeDir so
// it matches the engine's resolution; it is skipped when home is unavailable.
func TestRedactHomePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	home = strings.TrimRight(home, "/")

	s := &Sample{
		Meta: Meta{
			Source: home + "/.claude/projects/app/0c3f.jsonl",
			CWD:    home + "/app",
		},
		Turns: []Turn{
			{Role: RoleUser, Blocks: []Block{
				{Kind: KindText, Text: "open " + home + "/notes.txt"},
			}},
		},
	}

	Redact(s)

	if strings.Contains(s.Meta.Source, home) {
		t.Errorf("Meta.Source still contains home: %q", s.Meta.Source)
	}
	if !strings.HasPrefix(s.Meta.Source, "~") {
		t.Errorf("Meta.Source = %q, want a ~-prefixed path", s.Meta.Source)
	}
	if strings.Contains(s.Meta.CWD, home) {
		t.Errorf("Meta.CWD still contains home: %q", s.Meta.CWD)
	}
	if strings.Contains(s.Turns[0].Blocks[0].Text, home) {
		t.Errorf("Block text still contains home: %q", s.Turns[0].Blocks[0].Text)
	}
}

// TestRedactToolFields verifies that secrets in tool_use Input and tool_result
// Content are scrubbed too, not only Block.Text.
func TestRedactToolFields(t *testing.T) {
	s := &Sample{Turns: []Turn{
		{Role: RoleAssistant, Blocks: []Block{
			{Kind: KindToolUse, ToolUse: &ToolUse{
				ID:    "t1",
				Name:  "Bash",
				Input: []byte(`{"command":"export KEY=sk-ABCDEFGHIJKLMNOPQRSTUVWX"}`),
			}},
		}},
		{Role: RoleUser, Blocks: []Block{
			{Kind: KindToolResult, ToolRes: &ToolResult{
				ToolUseID: "t1",
				Content:   "leaked alice@example.com in output",
			}},
		}},
	}}

	subs := Redact(s)
	if subs < 2 {
		t.Errorf("substitutions = %d, want at least 2", subs)
	}
	if strings.Contains(string(s.Turns[0].Blocks[0].ToolUse.Input), "sk-ABCDEF") {
		t.Errorf("tool_use input still leaks key: %q", s.Turns[0].Blocks[0].ToolUse.Input)
	}
	if strings.Contains(s.Turns[1].Blocks[0].ToolRes.Content, "alice@example.com") {
		t.Errorf("tool_result still leaks email: %q", s.Turns[1].Blocks[0].ToolRes.Content)
	}
}

// TestRedactToolInputStaysValidJSON guards a regression: redacting a tool_use
// Input must not corrupt its JSON. The anthropic encoder emits Input verbatim as
// a json.RawMessage, so textual substitution that splits a "\\" escape pair (for
// example a redacted home path next to an escaped bracket "\\[") would leave a
// dangling backslash and make the encoder fail to marshal. After redaction the
// Input must still be valid JSON and the anthropic encoder must serialize it.
func TestRedactToolInputStaysValidJSON(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	home = strings.TrimRight(home, "/")

	input, err := json.Marshal(map[string]string{
		"pattern": `\[abc\]`,
		"path":    home + "/notes.txt",
		"contact": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	s := &Sample{Turns: []Turn{
		{Role: RoleAssistant, Blocks: []Block{
			{Kind: KindToolUse, ToolUse: &ToolUse{ID: "t1", Name: "Grep", Input: input}},
		}},
	}}

	if n := Redact(s); n == 0 {
		t.Fatalf("Redact made no substitutions, want home + email redacted")
	}
	got := s.Turns[0].Blocks[0].ToolUse.Input
	if !json.Valid(got) {
		t.Fatalf("redacted tool_use input is not valid JSON: %q", got)
	}
	if strings.Contains(string(got), home) {
		t.Errorf("redacted input still contains home: %q", got)
	}
	if strings.Contains(string(got), "alice@example.com") {
		t.Errorf("redacted input still leaks email: %q", got)
	}

	// The anthropic encoder emits Input verbatim; it must not fail to marshal.
	enc, err := NewEncoder(FormatAnthropic)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, s); err != nil {
		t.Fatalf("anthropic encode after redaction: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("anthropic record is not valid JSON: %s", buf.Bytes())
	}
}

// TestRedactNil verifies Redact handles a nil Sample without panicking.
func TestRedactNil(t *testing.T) {
	if got := Redact(nil); got != 0 {
		t.Errorf("Redact(nil) = %d, want 0", got)
	}
}
