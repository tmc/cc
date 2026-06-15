package antigravityread

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsSessionPath(t *testing.T) {
	tmp := t.TempDir()

	// Test directory path.
	dirPath := filepath.Join(tmp, "session-1")
	if err := os.MkdirAll(filepath.Join(dirPath, ".system_generated", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, ".system_generated", "logs", "transcript.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsSessionPath(dirPath) {
		t.Errorf("IsSessionPath(%q) = false, want true for dir with transcript.jsonl", dirPath)
	}

	// Test file path.
	filePath := filepath.Join(dirPath, ".system_generated", "logs", "transcript.jsonl")
	if !IsSessionPath(filePath) {
		t.Errorf("IsSessionPath(%q) = false, want true for transcript.jsonl file", filePath)
	}

	// Test negative cases.
	otherFile := filepath.Join(tmp, "other.txt")
	if err := os.WriteFile(otherFile, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if IsSessionPath(otherFile) {
		t.Errorf("IsSessionPath(%q) = true, want false for non-transcript file", otherFile)
	}
}

func TestReadTranscriptFile(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "fc83aa36-fb33-4d51-bcbb-c688fd797b11")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write workspace code-workspace file.
	wsContent := `{"folders":[{"path":"/Users/tmc/workspace"}]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "session.code-workspace"), []byte(wsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Hello antigravity"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Thinking...","tool_calls":[{"name":"run_command","args":"\"{\\\"CommandLine\\\":\\\"ls\\\"}\""}]}
{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","status":"DONE","created_at":"2026-06-12T03:22:50Z","content":"Created At: 2026-06-12T03:22:50Z\nCompleted At: 2026-06-12T03:22:50Z\n\n\t\t\t\tThe command completed successfully.\n\t\t\t\tOutput:\nfile1\nfile2\n"}
`
	transcriptPath := filepath.Join(logDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// Should have: session_meta, user (hello), assistant (thinking + tool_use), user (tool_result)
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	// Verify session_meta
	if entries[0].Type != "session_meta" || entries[0].CWD != "/Users/tmc/workspace" {
		t.Errorf("invalid session_meta: %+v", entries[0])
	}

	// Verify user input
	if entries[1].Type != "user" || entries[1].Message == nil || !strings.Contains(string(entries[1].Message.Content), "Hello") {
		t.Errorf("invalid user entry: %+v", entries[1])
	}

	// Verify assistant thinking and tool_use
	if entries[2].Type != "assistant" || entries[2].Message == nil {
		t.Errorf("invalid assistant entry: %+v", entries[2])
	}

	// Verify tool result
	if entries[3].Type != "user" || entries[3].ToolUseResult == nil || !entries[3].ToolUseResult.Success || !strings.Contains(entries[3].ToolUseResult.Stdout, "file1") {
		t.Errorf("invalid tool result entry: %+v", entries[3])
	}
}

func TestReadLegacySession(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "legacy-session-123")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(sessionDir, "task.md"), []byte("# Task description"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "implementation_plan.md"), []byte("# Plan details"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile legacy failed: %v", err)
	}

	// Should have: session_meta, user (task.md), assistant (implementation_plan.md)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Type != "session_meta" {
		t.Errorf("expected session_meta first, got %s", entries[0].Type)
	}
	if entries[1].Type != "user" || !strings.Contains(string(entries[1].Message.Content), "Task description") {
		t.Errorf("invalid user/task entry: %+v", entries[1])
	}
	if entries[2].Type != "assistant" || !strings.Contains(string(entries[2].Message.Content), "Plan details") {
		t.Errorf("invalid assistant/plan entry: %+v", entries[2])
	}
}
