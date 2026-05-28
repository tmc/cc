package cc

import (
	"context"
	"reflect"
	"testing"
)

func TestInboxAppendAndRead(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", t.TempDir())

	want := []InboxMessage{
		{From: "alice", Text: "hello", Summary: "greeting"},
		{From: "bob", Text: "ack", Summary: "ack"},
	}
	for _, m := range want {
		if err := AppendInbox(context.Background(),"review", "lead", m); err != nil {
			t.Fatalf("AppendInbox: %v", err)
		}
	}

	got, err := ReadInbox(context.Background(),"review", "lead")
	if err != nil {
		t.Fatalf("ReadInbox: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ReadInbox returned %d messages, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].From != want[i].From || got[i].Text != want[i].Text {
			t.Errorf("message %d: got %+v, want %+v", i, got[i], want[i])
		}
		if got[i].Timestamp == "" {
			t.Errorf("message %d: AppendInbox did not stamp Timestamp", i)
		}
	}
}

func TestInboxReadMissing(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", t.TempDir())
	got, err := ReadInbox(context.Background(),"nope", "nobody")
	if err != nil {
		t.Fatalf("ReadInbox(missing): %v", err)
	}
	if got != nil {
		t.Errorf("ReadInbox(missing) = %v, want nil", got)
	}
}

func TestReadUnreadMarksMessages(t *testing.T) {
	t.Setenv("CC_TEAMS_DIR", t.TempDir())

	for _, m := range []InboxMessage{
		{From: "x", Text: "one"},
		{From: "x", Text: "two"},
		{From: "x", Text: "three"},
	} {
		if err := AppendInbox(context.Background(),"t", "a", m); err != nil {
			t.Fatal(err)
		}
	}

	first, err := ReadUnread(context.Background(),"t", "a")
	if err != nil {
		t.Fatalf("ReadUnread (1): %v", err)
	}
	if len(first) != 3 {
		t.Errorf("first ReadUnread = %d messages, want 3", len(first))
	}

	second, err := ReadUnread(context.Background(),"t", "a")
	if err != nil {
		t.Fatalf("ReadUnread (2): %v", err)
	}
	if second != nil {
		t.Errorf("second ReadUnread = %v, want nil", second)
	}

	all, err := ReadInbox(context.Background(),"t", "a")
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range all {
		if !m.Read {
			t.Errorf("message %d not marked Read after ReadUnread", i)
		}
	}
}

func TestParseMessageStructured(t *testing.T) {
	tests := []struct {
		name string
		text string
		want *StructuredMessage
	}{
		{
			name: "shutdown",
			text: `{"type":"shutdown_request","requestId":"r1","from":"alice","reason":"done"}`,
			want: &StructuredMessage{Type: "shutdown_request", RequestID: "r1", From: "alice", Reason: "done"},
		},
		{
			name: "no type",
			text: `{"foo":"bar"}`,
			want: nil,
		},
		{
			name: "not json",
			text: `hello there`,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseMessage(InboxMessage{Text: tt.text})
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseMessage(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}
