package collector

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

func bashToolUseEntry(role, command string, ts time.Time) cc.Entry {
	input, _ := json.Marshal(map[string]string{"command": command})
	blocks := []cc.ContentBlock{
		{Type: "tool_use", Name: "Bash", Input: input},
	}
	content, _ := json.Marshal(blocks)
	return cc.Entry{
		Timestamp: ts,
		Message: &cc.Message{
			Role:    role,
			Content: content,
		},
	}
}

func toolResultEntry(stdout string) cc.Entry {
	return cc.Entry{
		ToolUseResult: &cc.ToolUseResult{
			Stdout: stdout,
		},
	}
}

func TestExtractLinks(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	targetSID := "D9DD130C-09DB-4B7B-BAB0-42B9A8CDF0FB"
	selfSID := "AABBCCDD-1234-5678-9ABC-DEF012345678"

	tests := []struct {
		name      string
		entries   []cc.Entry
		wantCount int
		check     func(t *testing.T, links []cass.SessionLink)
	}{
		{
			name: "send-text link extracted",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "hello world"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].TargetSession != targetSID {
					t.Errorf("target = %q, want %q", links[0].TargetSession, targetSID)
				}
				if links[0].Kind != "message" {
					t.Errorf("kind = %q, want %q", links[0].Kind, "message")
				}
				if links[0].Action != "send-text" {
					t.Errorf("action = %q, want %q", links[0].Action, "send-text")
				}
				if links[0].Text != "hello world" {
					t.Errorf("text = %q, want %q", links[0].Text, "hello world")
				}
			},
		},
		{
			name: "send-key link extracted",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-key "`+targetSID+`" "Enter"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].Action != "send-key" {
					t.Errorf("action = %q, want %q", links[0].Action, "send-key")
				}
				if links[0].Kind != "message" {
					t.Errorf("kind = %q, want %q", links[0].Kind, "message")
				}
			},
		},
		{
			name: "get-screen observation extracted",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session get-screen "`+targetSID+`"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].Kind != "observation" {
					t.Errorf("kind = %q, want %q", links[0].Kind, "observation")
				}
				if links[0].Action != "get-screen" {
					t.Errorf("action = %q, want %q", links[0].Action, "get-screen")
				}
				if links[0].Text != "" {
					t.Errorf("text = %q, want empty for observation", links[0].Text)
				}
			},
		},
		{
			name: "get-buffer observation extracted",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session get-buffer "`+targetSID+`"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].Action != "get-buffer" {
					t.Errorf("action = %q, want %q", links[0].Action, "get-buffer")
				}
			},
		},
		{
			name: "user messages ignored",
			entries: []cc.Entry{
				bashToolUseEntry("user", `it2 session send-text "`+targetSID+`" "should be ignored"`, ts),
			},
			wantCount: 0,
		},
		{
			name: "duplicate targets deduplicated",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "first"`, ts),
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "second"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].Text != "first" {
					t.Errorf("text = %q, want %q (first occurrence)", links[0].Text, "first")
				}
			},
		},
		{
			name: "same target different actions not deduplicated",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "msg"`, ts),
				bashToolUseEntry("assistant", `it2 session get-screen "`+targetSID+`"`, ts),
			},
			wantCount: 2,
		},
		{
			name: "source session ID extracted from tool result",
			entries: []cc.Entry{
				toolResultEntry(`[it2:send-text src=` + selfSID + ` dst=` + targetSID + `]`),
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "hello"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].SourceSession != selfSID {
					t.Errorf("source = %q, want %q", links[0].SourceSession, selfSID)
				}
			},
		},
		{
			name: "source session ID from current command",
			entries: []cc.Entry{
				toolResultEntry(selfSID + "\n"),
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "hello"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				if links[0].SourceSession != selfSID {
					t.Errorf("source = %q, want %q", links[0].SourceSession, selfSID)
				}
			},
		},
		{
			name: "empty entries",
			entries: nil,
			wantCount: 0,
		},
		{
			name: "entries without messages",
			entries: []cc.Entry{
				{Timestamp: ts},
				{ToolUseResult: &cc.ToolUseResult{Stdout: "some output"}},
			},
			wantCount: 0,
		},
		{
			name: "unquoted session ID",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-text `+targetSID+` "msg"`, ts),
			},
			wantCount: 1,
		},
		{
			name: "timestamp formatted in link",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "msg"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, links []cass.SessionLink) {
				want := "2025-01-15T10:30:00Z"
				if links[0].Timestamp != want {
					t.Errorf("timestamp = %q, want %q", links[0].Timestamp, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := ExtractLinks(tt.entries)
			if len(links) != tt.wantCount {
				t.Fatalf("got %d links, want %d: %+v", len(links), tt.wantCount, links)
			}
			if tt.check != nil {
				tt.check(t, links)
			}
		})
	}
}

// TestExtractLinks_FalsePositiveAvoidance verifies that it2 commands found
// in get-screen/get-buffer output (tool results) are not scanned for new
// target session IDs.
func TestExtractLinks_FalsePositiveAvoidance(t *testing.T) {
	otherSID := "11111111-2222-3333-4444-555555555555"
	targetSID := "D9DD130C-09DB-4B7B-BAB0-42B9A8CDF0FB"

	// Simulate: assistant runs get-screen, tool result contains another
	// session's send-text command. The send-text in the output should NOT
	// create a new link to otherSID.
	entries := []cc.Entry{
		bashToolUseEntry("assistant", `it2 session get-screen "`+targetSID+`"`, time.Now()),
		// Tool result contains commands from the observed session.
		{
			ToolUseResult: &cc.ToolUseResult{
				Stdout: `$ it2 session send-text "` + otherSID + `" "forwarded message"` + "\n" +
					"some screen content\n",
			},
		},
	}

	links := ExtractLinks(entries)

	for _, link := range links {
		if link.TargetSession == otherSID {
			t.Errorf("false positive: extracted link to %q from get-screen output", otherSID)
		}
	}

	// Should have exactly one link: the get-screen observation.
	if len(links) != 1 {
		t.Errorf("got %d links, want 1", len(links))
	}
	if len(links) > 0 && links[0].Action != "get-screen" {
		t.Errorf("action = %q, want %q", links[0].Action, "get-screen")
	}
}

func TestExtractItermSessionID(t *testing.T) {
	tests := []struct {
		name    string
		entries []cc.Entry
		want    string
	}{
		{
			name: "from src pattern",
			entries: []cc.Entry{
				toolResultEntry("[it2:send-text src=AABBCCDD-1234-5678-9ABC-DEF012345678 dst=11111111-2222-3333-4444-555555555555]"),
			},
			want: "AABBCCDD-1234-5678-9ABC-DEF012345678",
		},
		{
			name: "from get-screen src pattern",
			entries: []cc.Entry{
				toolResultEntry("[it2:get-screen src=AABBCCDD-1234-5678-9ABC-DEF012345678]"),
			},
			want: "AABBCCDD-1234-5678-9ABC-DEF012345678",
		},
		{
			name: "from session current output",
			entries: []cc.Entry{
				toolResultEntry("AABBCCDD-1234-5678-9ABC-DEF012345678\n"),
			},
			want: "AABBCCDD-1234-5678-9ABC-DEF012345678",
		},
		{
			name: "no tool results",
			entries: []cc.Entry{
				{Timestamp: time.Now()},
			},
			want: "",
		},
		{
			name: "empty entries",
			entries: nil,
			want:    "",
		},
		{
			name: "tool result without stdout",
			entries: []cc.Entry{
				{ToolUseResult: &cc.ToolUseResult{Stderr: "error"}},
			},
			want: "",
		},
		{
			name: "unrelated stdout content",
			entries: []cc.Entry{
				toolResultEntry("just some random output\nnothing to see here"),
			},
			want: "",
		},
		{
			name: "first match wins",
			entries: []cc.Entry{
				toolResultEntry("[it2:send-text src=AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE dst=11111111-2222-3333-4444-555555555555]"),
				toolResultEntry("[it2:send-text src=11111111-2222-3333-4444-555555555555 dst=AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE]"),
			},
			want: "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractItermSessionID(tt.entries)
			if got != tt.want {
				t.Errorf("extractItermSessionID() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractBashCommand(t *testing.T) {
	tests := []struct {
		name  string
		input json.RawMessage
		want  string
	}{
		{
			name:  "object with command field",
			input: json.RawMessage(`{"command":"echo hello"}`),
			want:  "echo hello",
		},
		{
			name:  "plain string",
			input: json.RawMessage(`"echo hello"`),
			want:  "echo hello",
		},
		{
			name:  "fallback to raw with escaped quotes",
			input: json.RawMessage(`{invalid json but has \"echo hello\"}`),
			want:  `{invalid json but has "echo hello"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBashCommand(tt.input)
			if got != tt.want {
				t.Errorf("extractBashCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractLinks_LongMessageTruncated(t *testing.T) {
	targetSID := "D9DD130C-09DB-4B7B-BAB0-42B9A8CDF0FB"
	longMsg := ""
	for i := 0; i < 250; i++ {
		longMsg += "x"
	}

	entries := []cc.Entry{
		bashToolUseEntry("assistant", `it2 session send-text "`+targetSID+`" "`+longMsg+`"`, time.Now()),
	}

	links := ExtractLinks(entries)
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if len(links[0].Text) > 203 { // 200 + "..."
		t.Errorf("text length = %d, want <= 203", len(links[0].Text))
	}
	if links[0].Text[len(links[0].Text)-3:] != "..." {
		t.Errorf("text should end with '...', got %q", links[0].Text[len(links[0].Text)-5:])
	}
}
