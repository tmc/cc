package cc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReadFileOpenCode(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".local", "share", "opencode", "storage", "session", "proj", "ses_core.json")
	writeTestFile(t, sessionPath, `{
  "id": "ses_core",
  "slug": "quiet-star",
  "version": "0.0.0-test",
  "projectID": "proj",
  "directory": "/work/cc",
  "title": "Core opencode",
  "time": {"created": 1780000000000, "updated": 1780000004000}
}`)
	writeTestFile(t, filepath.Join(root, ".local", "share", "opencode", "storage", "message", "ses_core", "msg_user.json"), `{
  "id": "msg_user",
  "sessionID": "ses_core",
  "role": "user",
  "time": {"created": 1780000001000},
  "model": {"providerID": "openrouter", "modelID": "moonshotai/kimi-k2.6"}
}`)
	writeTestFile(t, filepath.Join(root, ".local", "share", "opencode", "storage", "part", "msg_user", "prt_user.json"), `{
  "id": "prt_user",
  "sessionID": "ses_core",
  "messageID": "msg_user",
  "type": "text",
  "text": "hello opencode"
}`)
	writeTestFile(t, filepath.Join(root, ".local", "share", "opencode", "storage", "message", "ses_core", "msg_assistant.json"), `{
  "id": "msg_assistant",
  "sessionID": "ses_core",
  "role": "assistant",
  "time": {"created": 1780000002000},
  "modelID": "moonshotai/kimi-k2.6",
  "tokens": {"input": 10, "output": 3, "cache": {"read": 2, "write": 1}}
}`)
	writeTestFile(t, filepath.Join(root, ".local", "share", "opencode", "storage", "part", "msg_assistant", "prt_text.json"), `{
  "id": "prt_text",
  "sessionID": "ses_core",
  "messageID": "msg_assistant",
  "type": "text",
  "text": "done"
}`)
	writeTestFile(t, filepath.Join(root, ".local", "share", "opencode", "storage", "part", "msg_assistant", "prt_tool.json"), `{
  "id": "prt_tool",
  "sessionID": "ses_core",
  "messageID": "msg_assistant",
  "type": "tool",
  "callID": "toolu_read",
  "tool": "read",
  "state": {"input": {"file_path": "/work/cc/README.md"}, "output": "read ok"}
}`)

	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	sum := Summarize(sessionPath, entries)
	if sum.SessionID != "ses_core" {
		t.Fatalf("SessionID = %q, want ses_core", sum.SessionID)
	}
	if sum.CWD != "/work/cc" {
		t.Fatalf("CWD = %q, want /work/cc", sum.CWD)
	}
	if sum.FirstPrompt != "hello opencode" {
		t.Fatalf("FirstPrompt = %q, want hello opencode", sum.FirstPrompt)
	}
	if sum.AsstMessages != 1 || sum.UserMessages != 1 || sum.ToolUses != 1 {
		t.Fatalf("summary counts = user:%d assistant:%d tools:%d", sum.UserMessages, sum.AsstMessages, sum.ToolUses)
	}
	assistant := entries[len(entries)-1]
	if assistant.Message == nil {
		t.Fatal("assistant message missing")
	}
	uses := assistant.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "Read" {
		t.Fatalf("tool uses = %#v, want one Read", uses)
	}
	results := assistant.Message.ToolResults()
	if len(results) != 1 || results[0].ToolUseID != "toolu_read" || results[0].Content != "read ok" {
		t.Fatalf("tool results = %#v, want read ok for toolu_read", results)
	}
}

func TestReadFileOpenCodeToolErrorResult(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "opencode", "storage", "session", "proj", "ses_error.json")
	writeTestFile(t, sessionPath, `{"id":"ses_error","directory":"/work/cc","time":{"created":4102444800000}}`)
	writeTestFile(t, filepath.Join(root, "opencode", "storage", "message", "ses_error", "msg_assistant.json"), `{"id":"msg_assistant","sessionID":"ses_error","role":"assistant","time":{"created":4102444801000}}`)
	writeTestFile(t, filepath.Join(root, "opencode", "storage", "part", "msg_assistant", "prt_tool.json"), `{
  "id": "prt_tool",
  "sessionID": "ses_error",
  "messageID": "msg_assistant",
  "type": "tool",
  "tool": "bash",
  "state": {"input": {"command": "false"}, "error": "exit status 1"}
}`)

	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	assistant := entries[len(entries)-1]
	results := assistant.Message.ToolResults()
	if len(results) != 1 || !results[0].IsError || results[0].Content != "exit status 1" {
		t.Fatalf("tool results = %#v, want error result", results)
	}
}

func TestFindSessionFilesOpenCode(t *testing.T) {
	root := t.TempDir()
	opencodeHome := filepath.Join(root, "opencode")
	sessionPath := filepath.Join(opencodeHome, "storage", "session", "proj", "ses_find.json")
	writeTestFile(t, sessionPath, `{
  "id": "ses_find",
  "directory": "/work/find",
	"time": {"created": 1780000000000}
}`)
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("OPENCODE_HOME", opencodeHome)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi"))
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, "antigravity-empty"))
	if err := os.MkdirAll(filepath.Join(root, "claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := FindSessionFiles(context.Background(), 24*time.Hour, "find")
	if err != nil {
		t.Fatalf("FindSessionFiles: %v", err)
	}
	if len(got) != 1 || got[0] != sessionPath {
		t.Fatalf("FindSessionFiles = %v, want [%s]", got, sessionPath)
	}
}

func TestFindSessionFilesOpenCodeNestedActivity(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "opencode")
	sessionPath := filepath.Join(home, "storage", "session", "proj", "ses_nested.json")
	writeTestFile(t, sessionPath, `{"id":"ses_nested","directory":"/work/nested"}`)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(sessionPath, old, old); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "storage", "message", "ses_nested", "msg_new.json"), `{"id":"msg_new","sessionID":"ses_nested"}`)
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("OPENCODE_HOME", home)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi"))
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, "antigravity"))
	if err := os.MkdirAll(filepath.Join(root, "claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindSessionFiles(context.Background(), 24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsPath(got, sessionPath) {
		t.Fatalf("FindSessionFiles omitted active nested session: %v", got)
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeOffsetReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "storage", "session", "proj", "ses_tail.json")
	writeTestFile(t, path, `{"id":"ses_tail","directory":"/work","title":"tail"}`)
	message := filepath.Join(root, "storage", "message", "ses_tail", "msg_1.json")
	writeTestFile(t, message, `{"id":"msg_1","sessionID":"ses_tail","role":"user"}`)
	writeTestFile(t, filepath.Join(root, "storage", "part", "msg_1", "prt_1.json"), `{"id":"prt_1","type":"text","text":"first"}`)
	want, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name        string
		offset      int64
		withOffset  bool
		wantInvalid bool
	}{
		{"from start", 0, false, false},
		{"with offset", 0, true, false},
		{"end", info.Size(), false, true},
		{"middle", 1, false, true},
		{"past end", info.Size() + 1, false, true},
		{"negative", -1, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got []Entry
			var offset int64
			var partial *Entry
			var err error
			if tt.withOffset {
				got, offset, partial, err = ReadFileWithOffset(context.Background(), path)
			} else {
				got, offset, partial, err = ReadFileFrom(context.Background(), path, tt.offset)
			}
			if tt.wantInvalid {
				if !errors.Is(err, ErrTailInvalid) {
					t.Fatalf("error = %v, want ErrTailInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) || offset != info.Size() || partial != nil {
				t.Fatalf("read = %#v, %d, %v; want full session at %d", got, offset, partial, info.Size())
			}
		})
	}
	// A message changes without changing the metadata file. A byte-offset
	// cache must be invalidated so the replacement snapshot includes it.
	writeTestFile(t, filepath.Join(root, "storage", "part", "msg_1", "prt_1.json"), `{"id":"prt_1","type":"text","text":"updated"}`)
	if _, _, _, err := ReadFileFrom(context.Background(), path, info.Size()); !errors.Is(err, ErrTailInvalid) {
		t.Fatalf("changed part: %v", err)
	}
	got, _, _, err := ReadFileFrom(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(got, want) {
		t.Fatal("snapshot did not include changed part")
	}
}
