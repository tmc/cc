package cc

import (
	"context"
	"strings"
	"testing"
)

func TestDecodeEntryLineFormats(t *testing.T) {
	for _, tt := range []struct {
		name, line, kind, text string
		ok                     bool
	}{
		{"native", `{"type":"user","message":{"role":"user","content":"hello"}}`, "user", "hello", true},
		{"escaped type", `{"type":"us\u0065r","message":{"role":"user","content":"hello"}}`, "user", "hello", true},
		{"normalized envelope name", `{"type":"session_meta","sessionId":"s"}`, "session_meta", "", true},
		{"codex", `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"codex"}]}}`, "user", "codex", true},
		{"ignored envelope", `{"type":"reasoning","payload":{}}`, "", "", false},
		{"malformed", `{"type":`, "", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := decodeEntryLine([]byte(tt.line))
			if ok != tt.ok || e.Type != tt.kind {
				t.Fatalf("decode = %q, %v; want %q, %v", e.Type, ok, tt.kind, tt.ok)
			}
			if tt.text != "" && (e.Message == nil || e.Message.TextContent() != tt.text) {
				t.Fatalf("message = %#v", e.Message)
			}
		})
	}
}

func TestReaderRetainsScannerContent(t *testing.T) {
	entries, err := ReadAll(context.Background(), strings.NewReader(`{"type":"user","message":{"role":"user","content":"first"}}
{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second"}]}}
{"type":"user","message":{"role":"user","content":"third"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}
	for i, want := range []string{"first", "second", "third"} {
		if got := entries[i].Message.TextContent(); got != want {
			t.Errorf("entry %d = %q, want %q", i, got, want)
		}
	}
}
