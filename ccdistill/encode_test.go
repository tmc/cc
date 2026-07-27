package ccdistill

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// sampleForEncoding is the shared Sample used across encoder tests. It mirrors
// the canonical example in the package doc: system, user, assistant(text +
// tool_use), user(tool_result), assistant(text).
func sampleForEncoding() *Sample {
	return &Sample{
		Meta: Meta{
			SessionID:   "0c3f",
			Source:      "~/.claude/projects/app/0c3f.jsonl",
			Model:       "claude-opus-4",
			CWD:         "~/app",
			GitBranch:   "main",
			FirstPrompt: "add a -v flag to the CLI",
			NumTurns:    5,
			NumToolUses: 1,
		},
		Turns: []Turn{
			{Role: RoleSystem, Blocks: []Block{{Kind: KindText, Text: "be a coding agent"}}},
			{Role: RoleUser, Blocks: []Block{{Kind: KindText, Text: "add a -v flag to the CLI"}}},
			{Role: RoleAssistant, Blocks: []Block{
				{Kind: KindText, Text: "I'll read main.go first."},
				{Kind: KindToolUse, ToolUse: &ToolUse{
					ID: "toolu_01", Name: "Read", Input: json.RawMessage(`{"file_path":"main.go"}`),
				}},
			}},
			{Role: RoleUser, Blocks: []Block{
				{Kind: KindToolResult, ToolRes: &ToolResult{ToolUseID: "toolu_01", Content: "package main"}},
			}},
			{Role: RoleAssistant, Blocks: []Block{{Kind: KindText, Text: "Done. Added a -v flag."}}},
		},
	}
}

// encodeToMap runs an encoder and round-trips its output back into a generic
// map so tests assert key fields without brittle string equality.
func encodeToMap(t *testing.T, f Format) map[string]any {
	t.Helper()
	enc, err := NewEncoder(f)
	if err != nil {
		t.Fatalf("NewEncoder(%q): %v", f, err)
	}
	var buf bytes.Buffer
	if err := enc.Encode(&buf, sampleForEncoding()); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Encode must not append a trailing newline.
	if strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("encoder %q appended a trailing newline", f)
	}
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	return m
}

func TestNewEncoderUnknown(t *testing.T) {
	if _, err := NewEncoder(Format("bogus")); err == nil {
		t.Fatal("NewEncoder(bogus) = nil error, want error")
	}
}

// TestEncodeAnthropic asserts the native Anthropic shape: top-level system,
// messages[] with tool_use/tool_result blocks kept verbatim, and meta.
func TestEncodeAnthropic(t *testing.T) {
	m := encodeToMap(t, FormatAnthropic)

	if got, _ := m["system"].(string); got != "be a coding agent" {
		t.Errorf("system = %q, want %q", got, "be a coding agent")
	}

	meta, ok := m["meta"].(map[string]any)
	if !ok {
		t.Fatalf("meta missing or wrong type: %T", m["meta"])
	}
	if meta["session_id"] != "0c3f" {
		t.Errorf("meta.session_id = %v, want 0c3f", meta["session_id"])
	}

	msgs, ok := m["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing or wrong type: %T", m["messages"])
	}
	// system is hoisted out, so 4 messages remain.
	if len(msgs) != 4 {
		t.Fatalf("messages count = %d, want 4", len(msgs))
	}

	// First message: user with a single text block.
	first := msgs[0].(map[string]any)
	if first["role"] != RoleUser {
		t.Errorf("messages[0].role = %v, want user", first["role"])
	}

	// Second message: assistant with text + tool_use blocks.
	asst := msgs[1].(map[string]any)
	if asst["role"] != RoleAssistant {
		t.Errorf("messages[1].role = %v, want assistant", asst["role"])
	}
	content := asst["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content blocks = %d, want 2", len(content))
	}
	tu := content[1].(map[string]any)
	if tu["type"] != "tool_use" {
		t.Errorf("block type = %v, want tool_use", tu["type"])
	}
	if tu["name"] != "Read" || tu["id"] != "toolu_01" {
		t.Errorf("tool_use = %v, want name=Read id=toolu_01", tu)
	}
	if tu["input"] == nil {
		t.Error("tool_use input missing")
	}

	// Third message: user with a tool_result block.
	tr := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" {
		t.Errorf("block type = %v, want tool_result", tr["type"])
	}
	if tr["tool_use_id"] != "toolu_01" {
		t.Errorf("tool_result.tool_use_id = %v, want toolu_01", tr["tool_use_id"])
	}
	if tr["content"] != "package main" {
		t.Errorf("tool_result.content = %v", tr["content"])
	}
}

func TestDecodeAnthropicRoundTrip(t *testing.T) {
	encoder, err := NewEncoder(FormatAnthropic)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := encoder.Encode(&encoded, sampleForEncoding()); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeAnthropic(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	want := sampleForEncoding()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded sample differs:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDecodeAnthropicRejectsMalformedToolBlock(t *testing.T) {
	input := `{"messages":[{"role":"user","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}],"meta":{}}`
	if _, err := DecodeAnthropic(strings.NewReader(input)); err == nil {
		t.Fatal("DecodeAnthropic accepted user tool_use block")
	}
}

func TestDecodeAnthropicRejectsUnknownField(t *testing.T) {
	input := `{"messages":[],"meta":{},"unexpected":true}`
	if _, err := DecodeAnthropic(strings.NewReader(input)); err == nil {
		t.Fatal("DecodeAnthropic accepted unknown field")
	}
}

// TestEncodeOpenAI asserts {messages:[{role,content}]} with tool calls and
// results flattened into the message text.
func TestEncodeOpenAI(t *testing.T) {
	m := encodeToMap(t, FormatOpenAI)

	msgs, ok := m["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing or wrong type: %T", m["messages"])
	}
	if len(msgs) != 5 {
		t.Fatalf("messages count = %d, want 5 (system retained as a message)", len(msgs))
	}

	roles := []string{RoleSystem, RoleUser, RoleAssistant, RoleUser, RoleAssistant}
	for i, want := range roles {
		msg := msgs[i].(map[string]any)
		if msg["role"] != want {
			t.Errorf("messages[%d].role = %v, want %v", i, msg["role"], want)
		}
		if _, ok := msg["content"].(string); !ok {
			t.Errorf("messages[%d].content is not a string: %T", i, msg["content"])
		}
	}

	// The assistant turn flattens the tool_use into its content text.
	asstContent := msgs[2].(map[string]any)["content"].(string)
	if !strings.Contains(asstContent, "tool_use") || !strings.Contains(asstContent, "Read") {
		t.Errorf("assistant content did not flatten tool_use: %q", asstContent)
	}
	// The tool_result turn flattens the result into its content text.
	trContent := msgs[3].(map[string]any)["content"].(string)
	if !strings.Contains(trContent, "tool_result") || !strings.Contains(trContent, "package main") {
		t.Errorf("user content did not flatten tool_result: %q", trContent)
	}

	if _, ok := m["meta"].(map[string]any); !ok {
		t.Errorf("meta missing from openai record")
	}
}

// TestEncodeShareGPT asserts {conversations:[{from,value}]} with ShareGPT role
// names (system, human, gpt).
func TestEncodeShareGPT(t *testing.T) {
	m := encodeToMap(t, FormatShareGPT)

	convs, ok := m["conversations"].([]any)
	if !ok {
		t.Fatalf("conversations missing or wrong type: %T", m["conversations"])
	}
	if len(convs) != 5 {
		t.Fatalf("conversations count = %d, want 5", len(convs))
	}

	wantFrom := []string{"system", "human", "gpt", "human", "gpt"}
	for i, want := range wantFrom {
		c := convs[i].(map[string]any)
		if c["from"] != want {
			t.Errorf("conversations[%d].from = %v, want %v", i, c["from"], want)
		}
		if _, ok := c["value"].(string); !ok {
			t.Errorf("conversations[%d].value is not a string: %T", i, c["value"])
		}
	}

	if _, ok := m["meta"].(map[string]any); !ok {
		t.Errorf("meta missing from sharegpt record")
	}
}

// TestEncodeMetaHarness verifies the Meta.Harness field is carried through into
// every encoded record's meta object when set. It is a fixed enum token, not a
// path or secret, so it is emitted verbatim by all three encoders.
func TestEncodeMetaHarness(t *testing.T) {
	for _, f := range []Format{FormatAnthropic, FormatOpenAI, FormatShareGPT} {
		t.Run(string(f), func(t *testing.T) {
			enc, err := NewEncoder(f)
			if err != nil {
				t.Fatalf("NewEncoder(%q): %v", f, err)
			}
			s := sampleForEncoding()
			s.Meta.Harness = "codex"
			var buf bytes.Buffer
			if err := enc.Encode(&buf, s); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
				t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
			}
			meta, ok := m["meta"].(map[string]any)
			if !ok {
				t.Fatalf("meta missing or wrong type: %T", m["meta"])
			}
			if got, ok := meta["harness"]; !ok {
				t.Errorf("meta.harness key absent; want it present when Harness is set")
			} else if got != "codex" {
				t.Errorf("meta.harness = %v, want codex", got)
			}
		})
	}
}

// TestEncodeMetaHarnessOmitEmpty guards the backward-compatibility contract: an
// unset Harness (the empty string) is omitted via omitempty, so records that
// never set it stay byte-identical to the pre-Harness encoding and stdin records
// (which have no on-disk path) carry no misleading harness key.
func TestEncodeMetaHarnessOmitEmpty(t *testing.T) {
	for _, f := range []Format{FormatAnthropic, FormatOpenAI, FormatShareGPT} {
		t.Run(string(f), func(t *testing.T) {
			enc, err := NewEncoder(f)
			if err != nil {
				t.Fatalf("NewEncoder(%q): %v", f, err)
			}
			s := sampleForEncoding() // Meta.Harness left empty.
			var buf bytes.Buffer
			if err := enc.Encode(&buf, s); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if strings.Contains(buf.String(), "harness") {
				t.Errorf("empty Harness leaked a harness key: %s", buf.String())
			}
		})
	}
}

// TestEncodeDanglingToolUse confirms encoders do not panic and produce sensible
// output when a tool_use has no matching tool_result.
func TestEncodeDanglingToolUse(t *testing.T) {
	s := &Sample{
		Meta: Meta{SessionID: "x", NumTurns: 2, NumToolUses: 1},
		Turns: []Turn{
			{Role: RoleUser, Blocks: []Block{{Kind: KindText, Text: "go"}}},
			{Role: RoleAssistant, Blocks: []Block{
				{Kind: KindText, Text: "reading"},
				{Kind: KindToolUse, ToolUse: &ToolUse{ID: "orphan", Name: "Read", Input: json.RawMessage(`{}`)}},
			}},
		},
	}
	for _, f := range []Format{FormatAnthropic, FormatOpenAI, FormatShareGPT} {
		t.Run(string(f), func(t *testing.T) {
			enc, err := NewEncoder(f)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}
			var buf bytes.Buffer
			if err := enc.Encode(&buf, s); err != nil {
				t.Fatalf("Encode: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
				t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
			}
			// The anthropic encoder keeps the tool_use id verbatim; the
			// openai/sharegpt encoders flatten the call to text using the
			// tool name. In all three the dangling tool_use must survive.
			out := buf.String()
			if f == FormatAnthropic {
				if !strings.Contains(out, "orphan") {
					t.Errorf("anthropic dropped dangling tool_use id: %s", out)
				}
			}
			if !strings.Contains(out, "Read") {
				t.Errorf("dangling tool_use dropped from %q output: %s", f, out)
			}
		})
	}
}
