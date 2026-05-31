package collector

import (
	"testing"

	"github.com/tmc/cc"
)

func TestWorkspaceFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "standard absolute path",
			path: "/home/user/.claude/projects/-Volumes-tmc-go-src-github-com-tmc-cc/abc123.jsonl",
			// decodePath checks filesystem: /Volumes/tmc/go/src/github.com exists
			// so "github-com" resolves to "github.com" not "github/com".
			want: "/Volumes/tmc/go/src/github.com/tmc/cc",
		},
		{
			name: "home directory path",
			path: "/home/user/.claude/projects/-home-user-myproject/session.jsonl",
			want: "/home/user/myproject",
		},
		{
			name: "nested session file",
			path: "/home/user/.claude/projects/-tmp-work/subdir/session.jsonl",
			want: "/tmp/work",
		},
		{
			name: "relative encoded path without leading dash",
			path: "/home/user/.claude/projects/relative-project/session.jsonl",
			want: "relative/project",
		},
		{
			name: "no projects parent",
			path: "/some/random/path/session.jsonl",
			want: "",
		},
		{
			name: "file directly in projects dir",
			path: "/home/user/.claude/projects/session.jsonl",
			want: "",
		},
		{
			name: "single component path",
			path: "/home/user/.claude/projects/-root/session.jsonl",
			want: "/root",
		},
		{
			name: "subagent session nested under session-id dir",
			path: "/home/user/.claude/projects/-Volumes-tmc-myproject/7b720b28-662f-4c46/subagents/agent-a95e4e0.jsonl",
			want: "/Volumes/tmc/myproject",
		},
		{
			name: "tool-results file nested under session-id dir",
			path: "/home/user/.claude/projects/-Volumes-work/a4581d87-b3ee-474f/tool-results/toolu_abc.txt",
			want: "/Volumes/work",
		},
		{
			name: "macOS private var temp path encoding",
			path: "/home/user/.claude/projects/-private-var-folders-kj-abc-T-tmp-xyz/session.jsonl",
			want: "/private/var/folders/kj/abc/T/tmp/xyz",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workspaceFromPath(tt.path)
			if got != tt.want {
				t.Errorf("workspaceFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTitleFromSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary cc.SessionSummary
		want    string
	}{
		{
			name: "short prompt",
			summary: cc.SessionSummary{
				FirstPrompt: "Fix the login bug",
				File:        "/path/to/session.jsonl",
			},
			want: "Fix the login bug",
		},
		{
			name: "long prompt truncated at 80 chars",
			summary: cc.SessionSummary{
				FirstPrompt: "This is a very long prompt that exceeds eighty characters and should be truncated with an ellipsis at the end",
				File:        "/path/to/session.jsonl",
			},
			want: "This is a very long prompt that exceeds eighty characters and should be truncate...",
		},
		{
			name: "exactly 80 chars not truncated",
			summary: cc.SessionSummary{
				FirstPrompt: "12345678901234567890123456789012345678901234567890123456789012345678901234567890",
				File:        "/path/to/session.jsonl",
			},
			want: "12345678901234567890123456789012345678901234567890123456789012345678901234567890",
		},
		{
			name: "81 chars truncated",
			summary: cc.SessionSummary{
				FirstPrompt: "123456789012345678901234567890123456789012345678901234567890123456789012345678901",
				File:        "/path/to/session.jsonl",
			},
			want: "12345678901234567890123456789012345678901234567890123456789012345678901234567890...",
		},
		{
			name: "custom title preferred over prompt",
			summary: cc.SessionSummary{
				CustomTitle: "test-session-alpha",
				FirstPrompt: "Fix the login bug",
				File:        "/path/to/session.jsonl",
			},
			want: "test-session-alpha",
		},
		{
			name: "custom title preferred over long prompt",
			summary: cc.SessionSummary{
				CustomTitle: "my-project",
				FirstPrompt: "This is a very long prompt that exceeds eighty characters and should be truncated with an ellipsis at the end",
				File:        "/path/to/session.jsonl",
			},
			want: "my-project",
		},
		{
			name: "empty custom title falls through to prompt",
			summary: cc.SessionSummary{
				CustomTitle: "",
				FirstPrompt: "Fix the login bug",
				File:        "/path/to/session.jsonl",
			},
			want: "Fix the login bug",
		},
		{
			name: "empty prompt falls back to filename",
			summary: cc.SessionSummary{
				FirstPrompt: "",
				File:        "/home/user/.claude/projects/test/session-abc.jsonl",
			},
			want: "session-abc.jsonl",
		},
		{
			name: "command markup",
			summary: cc.SessionSummary{
				FirstPrompt: "<command-name>efforts</command-name> <command-message>efforts</command-message>",
				File:        "/path/to/session.jsonl",
			},
			want: "efforts",
		},
		{
			name: "multiline command markup",
			summary: cc.SessionSummary{
				FirstPrompt: "<command-name>\nefforts\n</command-name> <command-message>\nreview branch\n</command-message>",
				File:        "/path/to/session.jsonl",
			},
			want: "review branch",
		},
		{
			name: "goal context objective",
			summary: cc.SessionSummary{
				FirstPrompt: "<goal_context>\n<objective>\nflesh this tool out to be complete\n</objective>\n</goal_context>",
				File:        "/path/to/session.jsonl",
			},
			want: "goal: flesh this tool out to be complete",
		},
		{
			name: "goal context markup",
			summary: cc.SessionSummary{
				FirstPrompt: "<goal_context> Continue working toward the active thread. </goal_context>",
				File:        "/path/to/session.jsonl",
			},
			want: "Continue working toward the active thread.",
		},
		{
			name: "image payload markup",
			summary: cc.SessionSummary{
				FirstPrompt: "<image name=[Image #1]> input_image data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA",
				File:        "/path/to/session.jsonl",
			},
			want: "image input",
		},
		{
			name: "empty prompt with empty file",
			summary: cc.SessionSummary{
				FirstPrompt: "",
				File:        "",
			},
			want: ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := titleFromSummary(tt.summary)
			if got != tt.want {
				t.Errorf("titleFromSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionID(t *testing.T) {
	// Same input should always produce the same ID.
	id1 := sessionID("/path/to/session.jsonl")
	id2 := sessionID("/path/to/session.jsonl")
	if id1 != id2 {
		t.Errorf("sessionID not deterministic: %q != %q", id1, id2)
	}

	// Different inputs should produce different IDs.
	id3 := sessionID("/different/path.jsonl")
	if id1 == id3 {
		t.Errorf("sessionID collision: %q == %q for different paths", id1, id3)
	}

	// ID should be a 32-character hex string (16 bytes).
	if len(id1) != 32 {
		t.Errorf("sessionID length = %d, want 32", len(id1))
	}
}

func TestUnderSubagents(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/p/-work/uuid/subagents/workflows/wf_x/agent-a.jsonl", true},
		{"/p/-work/uuid/subagents/agent-a.jsonl", true},
		{"/p/-work/uuid.jsonl", false},
		{"/p/my-subagents-tool/uuid.jsonl", false}, // substring, not a segment
		{"/p/subagents", true},                     // the dir itself
	}
	for _, c := range cases {
		if got := underSubagents(c.path); got != c.want {
			t.Errorf("underSubagents(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
