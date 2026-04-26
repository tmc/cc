package cc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTaskNotification(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    TaskNotification
		wantOK  bool
	}{
		{
			name: "full",
			input: `<task-notification>
<task-id>a5510922c09908bfd</task-id>
<tool-use-id>toolu_019b25bfoTFGTVNhhz57X4BQ</tool-use-id>
<output-file>/private/tmp/.../a5510922c09908bfd.output</output-file>
<status>completed</status>
<summary>Agent "P0-1: CI test gate workflow" completed</summary>
<result>Done. Created the workflow.</result>
<usage><total_tokens>22407</total_tokens><tool_uses>8</tool_uses><duration_ms>53342</duration_ms></usage>
</task-notification>`,
			want: TaskNotification{
				TaskID:      "a5510922c09908bfd",
				ToolUseID:   "toolu_019b25bfoTFGTVNhhz57X4BQ",
				OutputFile:  "/private/tmp/.../a5510922c09908bfd.output",
				Status:      "completed",
				Summary:     `Agent "P0-1: CI test gate workflow" completed`,
				Result:      "Done. Created the workflow.",
				TotalTokens: 22407,
				ToolUses:    8,
				DurationMs:  53342,
			},
			wantOK: true,
		},
		{
			name: "with worktree",
			input: `<task-notification>
<task-id>aafeefef8ccd7d551</task-id>
<status>completed</status>
<usage><total_tokens>74674</total_tokens><tool_uses>55</tool_uses><duration_ms>249945</duration_ms></usage>
<worktree><worktreePath>/Volumes/tmc/go/src/github.com/tmc/nanoclaw/.claude/worktrees/agent-aafeefef</worktreePath><worktreeBranch>worktree-agent-aafeefef</worktreeBranch></worktree>
</task-notification>`,
			want: TaskNotification{
				TaskID:         "aafeefef8ccd7d551",
				Status:         "completed",
				TotalTokens:    74674,
				ToolUses:       55,
				DurationMs:     249945,
				WorktreePath:   "/Volumes/tmc/go/src/github.com/tmc/nanoclaw/.claude/worktrees/agent-aafeefef",
				WorktreeBranch: "worktree-agent-aafeefef",
			},
			wantOK: true,
		},
		{
			name: "minimal",
			input: `<task-notification>
<task-id>abc123</task-id>
</task-notification>`,
			want:   TaskNotification{TaskID: "abc123"},
			wantOK: true,
		},
		{
			name:   "plain user text",
			input:  "wait so when i look at disk utility is it misleading?",
			wantOK: false,
		},
		{
			name:   "empty",
			input:  "",
			wantOK: false,
		},
		{
			name:   "whitespace only",
			input:  "   \n  \t",
			wantOK: false,
		},
		{
			name:   "malformed XML",
			input:  "<task-notification><task-id>broken",
			wantOK: false,
		},
		{
			name: "leading whitespace",
			input: `
<task-notification>
<task-id>x</task-id>
</task-notification>`,
			want:   TaskNotification{TaskID: "x"},
			wantOK: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseTaskNotification(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestParseTaskNotificationStrict_NotANotification(t *testing.T) {
	_, err := ParseTaskNotificationStrict("plain text")
	if err == nil {
		t.Fatal("want error for plain text, got nil")
	}
}

func TestParseTaskNotificationStrict_Malformed(t *testing.T) {
	_, err := ParseTaskNotificationStrict("<task-notification><unclosed>")
	if err == nil {
		t.Fatal("want error for malformed XML, got nil")
	}
	if err == errNotTaskNotification {
		t.Fatal("malformed should not be reported as not-a-notification")
	}
}

func TestReadSubagentMeta(t *testing.T) {
	dir := t.TempDir()

	t.Run("full meta", func(t *testing.T) {
		path := filepath.Join(dir, "full.json")
		if err := os.WriteFile(path, []byte(`{"agentType":"general-purpose","description":"P0-1: CI test gate workflow"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadSubagentMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		want := SubagentMeta{AgentType: "general-purpose", Description: "P0-1: CI test gate workflow"}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("missing file is not an error", func(t *testing.T) {
		got, err := ReadSubagentMeta(filepath.Join(dir, "does-not-exist.json"))
		if err != nil {
			t.Fatalf("missing file should be nil error, got %v", err)
		}
		if got != (SubagentMeta{}) {
			t.Errorf("got %+v, want zero", got)
		}
	})

	t.Run("malformed json", func(t *testing.T) {
		path := filepath.Join(dir, "broken.json")
		if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSubagentMeta(path); err == nil {
			t.Fatal("want error for malformed json")
		}
	})

	t.Run("empty fields", func(t *testing.T) {
		path := filepath.Join(dir, "empty.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := ReadSubagentMeta(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != (SubagentMeta{}) {
			t.Errorf("got %+v, want zero", got)
		}
	})
}
