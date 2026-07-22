package ccmodel

import (
	"encoding/json"
	"testing"
)

func TestToolUseResultUnmarshal(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		wantText    string
		wantContent string
		wantStdout  string
	}{
		{
			name:       "object",
			data:       `{"stdout":"ok","status":"done"}`,
			wantStdout: "ok",
		},
		{
			name:     "bare string",
			data:     `"User rejected tool use"`,
			wantText: "User rejected tool use",
		},
		{
			name:     "string with escapes",
			data:     `"Error: String to replace not found in file.\nString: x"`,
			wantText: "Error: String to replace not found in file.\nString: x",
		},
		{
			name:        "content block array",
			data:        `[{"type":"text","text":"VALID"}]`,
			wantContent: `[{"type":"text","text":"VALID"}]`,
		},
		{
			name:        "object with array content",
			data:        `{"content":[{"type":"text","text":"hi"}]}`,
			wantContent: `[{"type":"text","text":"hi"}]`,
		},
		{
			name:     "leading whitespace before string",
			data:     "  \n\"rejected\"",
			wantText: "rejected",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ToolUseResult
			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", tt.data, err)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if string(got.Content) != tt.wantContent {
				t.Errorf("Content = %q, want %q", got.Content, tt.wantContent)
			}
			if got.Stdout != tt.wantStdout {
				t.Errorf("Stdout = %q, want %q", got.Stdout, tt.wantStdout)
			}
		})
	}
}

func TestContentBlockContentShapes(t *testing.T) {
	tests := []struct {
		name        string
		data        string
		wantContent string
		wantRaw     string
	}{
		{
			name:        "string content",
			data:        `{"type":"tool_result","content":"exit 0"}`,
			wantContent: "exit 0",
		},
		{
			name:        "array of text blocks",
			data:        `{"type":"tool_result","content":[{"type":"text","text":"hello"}]}`,
			wantContent: "hello",
			wantRaw:     `[{"type":"text","text":"hello"}]`,
		},
		{
			// No "text" key, so ExtractAnyText falls back to every string
			// value. ContentRaw keeps the exact payload either way.
			name:        "array with no text payload",
			data:        `{"type":"tool_result","content":[{"type":"tool_reference","tool_name":"WebSearch"}]}`,
			wantContent: "WebSearch\ntool_reference",
			wantRaw:     `[{"type":"tool_reference","tool_name":"WebSearch"}]`,
		},
		{
			name: "absent content",
			data: `{"type":"text","text":"hi"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ContentBlock
			if err := json.Unmarshal([]byte(tt.data), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", tt.data, err)
			}
			if got.Content != tt.wantContent {
				t.Errorf("Content = %q, want %q", got.Content, tt.wantContent)
			}
			if string(got.ContentRaw) != tt.wantRaw {
				t.Errorf("ContentRaw = %q, want %q", got.ContentRaw, tt.wantRaw)
			}
		})
	}
}

// TestMessageContentBlocksSurvivesArrayContent checks that one block with an
// array-shaped content field does not discard the other blocks in the message.
func TestMessageContentBlocksSurvivesArrayContent(t *testing.T) {
	m := &Message{Role: "user", Content: json.RawMessage(`[
		{"type":"text","text":"before"},
		{"type":"tool_result","content":[{"type":"tool_reference","tool_name":"WebSearch"}]},
		{"type":"text","text":"after"}
	]`)}
	blocks := m.ContentBlocks()
	if len(blocks) != 3 {
		t.Fatalf("ContentBlocks() returned %d blocks, want 3 (array content discarded the message)", len(blocks))
	}
	if blocks[0].Text != "before" || blocks[2].Text != "after" {
		t.Errorf("surrounding blocks lost: %q, %q", blocks[0].Text, blocks[2].Text)
	}
	if len(blocks[1].ContentRaw) == 0 {
		t.Error("tool_result ContentRaw not preserved")
	}
}

// TestEntryToolUseResultShapes checks that an entry survives every
// toolUseResult shape. A strict object decode fails on the string and array
// forms, which would discard the whole entry along with its uuid and
// timestamp.
func TestEntryToolUseResultShapes(t *testing.T) {
	for _, shape := range []string{
		`"User rejected tool use"`,
		`[{"type":"text","text":"VALID"}]`,
		`{"stdout":"ok"}`,
	} {
		line := `{"uuid":"u1","type":"user","toolUseResult":` + shape + `}`
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("Unmarshal entry with toolUseResult %s: %v", shape, err)
		}
		if e.UUID != "u1" {
			t.Errorf("UUID = %q, want u1 (entry lost for shape %s)", e.UUID, shape)
		}
		if e.ToolUseResult == nil {
			t.Errorf("ToolUseResult = nil for shape %s", shape)
		}
	}
}

func TestAttachmentRoundTripUnknownFields(t *testing.T) {
	input := []byte(`{"type":"future_event","future":{"enabled":true}}`)
	var attachment Attachment
	if err := json.Unmarshal(input, &attachment); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["type"] != "future_event" {
		t.Fatalf("type = %v, want future_event", got["type"])
	}
	if future, ok := got["future"].(map[string]any); !ok || future["enabled"] != true {
		t.Fatalf("future = %#v, want enabled field", got["future"])
	}
}
