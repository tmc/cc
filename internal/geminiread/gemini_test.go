package geminiread

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIsSessionPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/Users/u/.gemini/tmp/proj/chats/session-2026-04-03T22-56-d00a4143.json", true},
		{"/Users/u/.gemini/tmp/proj/logs.json", false},
		{"/Users/u/.gemini/projects/-proj/abc.jsonl", false},
		{"/Users/u/.gemini/tmp/proj/chats/other.json", false},
	}
	for _, tt := range tests {
		if got := IsSessionPath(tt.path); got != tt.want {
			t.Errorf("IsSessionPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestReadFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("GEMINI_HOME", tmp)

	projDir := filepath.Join(tmp, "tmp", "myproj")
	chatsDir := filepath.Join(projDir, "chats")
	if err := os.MkdirAll(chatsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ".project_root"), []byte("/work/myproj\n"), 0644); err != nil {
		t.Fatal(err)
	}

	sessionJSON := `{
  "sessionId": "d00a4143-e123-42c2-a19f-874687c50621",
  "projectHash": "abc",
  "startTime": "2026-04-03T22:57:32.508Z",
  "lastUpdated": "2026-04-03T22:59:00.000Z",
  "messages": [
    {
      "id": "m0",
      "timestamp": "2026-04-03T22:57:32.508Z",
      "type": "info",
      "content": "Gemini CLI update available"
    },
    {
      "id": "m1",
      "timestamp": "2026-04-03T22:58:00.000Z",
      "type": "user",
      "content": [{"text": "hello gemini"}]
    },
    {
      "id": "m2",
      "timestamp": "2026-04-03T22:58:10.000Z",
      "type": "gemini",
      "model": "gemini-3.1-pro",
      "thoughts": [{"subject": "Plan", "description": "Read the file"}],
      "content": "Reading file now.",
      "toolCalls": [
        {
          "id": "tc-1",
          "name": "read_file",
          "args": {"file_path": "/work/myproj/main.go"},
          "result": [{"functionResponse": {"response": {"output": "package main"}}}],
          "status": "success"
        }
      ],
      "tokens": {"input": 100, "output": 20, "cached": 40, "thoughts": 15}
    }
  ]
}`
	sessionPath := filepath.Join(chatsDir, "session-2026-04-03T22-57-d00a4143.json")
	if err := os.WriteFile(sessionPath, []byte(sessionJSON), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4 (meta, user, assistant, tool_result)", len(entries))
	}
	if entries[0].CWD != "/work/myproj" || entries[0].SessionID != "d00a4143-e123-42c2-a19f-874687c50621" {
		t.Errorf("meta entry = %+v", entries[0])
	}
	if entries[1].Message.TextContent() != "hello gemini" {
		t.Errorf("user text = %q", entries[1].Message.TextContent())
	}
	asst := entries[2]
	if asst.Message.Model != "gemini-3.1-pro" {
		t.Errorf("model = %q, want gemini-3.1-pro", asst.Message.Model)
	}
	if asst.Message.Usage == nil || asst.Message.Usage.InputTokens != 100 || asst.Message.Usage.OutputTokens != 35 || asst.Message.Usage.CacheReadInputTokens != 40 {
		t.Errorf("usage = %+v", asst.Message.Usage)
	}
	tus := asst.Message.ToolUses()
	if len(tus) != 1 || tus[0].Name != "View" {
		t.Errorf("toolUses = %+v", tus)
	}
	trs := entries[3].Message.ToolResults()
	if len(trs) != 1 || trs[0].Content != "package main" || trs[0].IsError {
		t.Errorf("toolResults = %+v", trs)
	}
}

func TestReadNestedContent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GEMINI_HOME", dir)
	path := filepath.Join(dir, "session.json")
	const session = `{
		"sessionId":"s1",
		"messages":[
			{"type":"user","content":{"parts":[{"text":"first"},{"inlineData":{}},{"text":"second"}]}},
			{"type":"gemini","content":{"parts":[{"text":"answer"}]}}
		]
	}`
	if err := os.WriteFile(path, []byte(session), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for i, want := range []string{"first\nsecond", "answer"} {
		if got := entries[i].Message.TextContent(); got != want {
			t.Errorf("entry %d text = %q, want %q", i, got, want)
		}
	}
}
