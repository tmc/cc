package cc

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEntryPreservesExtendedJSONLFields(t *testing.T) {
	data := []byte(`{
		"type": "assistant",
		"uuid": "assistant-1",
		"thinkingMetadata": {"signature":"sig-1"},
		"todos": [{"content":"verify","status":"pending"}],
		"requestId": "req-1",
		"sourceToolAssistantUUID": "tool-parent-1"
	}`)
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}

	if entry.RequestID != "req-1" {
		t.Errorf("RequestID = %q, want req-1", entry.RequestID)
	}
	if entry.SourceToolAssistantUUID != "tool-parent-1" {
		t.Errorf("SourceToolAssistantUUID = %q, want tool-parent-1", entry.SourceToolAssistantUUID)
	}
	if !bytes.Contains(entry.ThinkingMetadata, []byte(`"signature"`)) {
		t.Errorf("ThinkingMetadata = %s, want signature field", entry.ThinkingMetadata)
	}
	if !bytes.Contains(entry.Todos, []byte(`"verify"`)) {
		t.Errorf("Todos = %s, want todo payload", entry.Todos)
	}
}
