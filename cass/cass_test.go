package cass

import (
	"testing"
	"time"
)

func TestSessionBasics(t *testing.T) {
	s := Session{
		ID:        "abc123",
		Agent:     "claude-code",
		Title:     "Test session",
		Workspace: "/tmp/test",
		StartedAt: time.Now().Add(-time.Hour),
		EndedAt:   time.Now(),
		Messages: []Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
		},
	}

	if s.ID != "abc123" {
		t.Errorf("got id %q, want %q", s.ID, "abc123")
	}
	if len(s.Messages) != 2 {
		t.Errorf("got %d messages, want 2", len(s.Messages))
	}
}

func TestSearchRequestDefaults(t *testing.T) {
	req := SearchRequest{Query: "test"}
	if req.Mode != SearchLexical {
		t.Errorf("default mode should be lexical")
	}
	if req.Limit != 0 {
		t.Errorf("default limit should be 0 (let store decide)")
	}
}
