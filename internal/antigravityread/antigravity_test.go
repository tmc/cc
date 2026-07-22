package antigravityread

import (
	"context"
	"fmt"
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
	wsContent := `{"folders":[{"path":"/Users/example/workspace"}]}`
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
	if entries[0].Type != "session_meta" || entries[0].CWD != "/Users/example/workspace" {
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

func TestReadTranscriptPairsEveryToolResult(t *testing.T) {
	entries, err := ReadFile(context.Background(), filepath.Join("testdata", "tool-results"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want meta, user, assistant, and two results", len(entries))
	}
	uses := entries[2].Message.ToolUses()
	if len(uses) != 2 || uses[0].Name != "Write" || uses[1].Name != "Bash" {
		t.Fatalf("tool uses = %#v", uses)
	}
	for i, want := range []string{uses[0].ID, uses[1].ID} {
		results := entries[i+3].Message.ToolResults()
		if len(results) != 1 || results[0].ToolUseID != want {
			t.Errorf("result %d = %#v, want tool id %q", i, results, want)
		}
	}
}

func TestAntigravitySessionID(t *testing.T) {
	for _, tt := range []struct{ in, want string }{{"abc", "ag-abc"}, {"ag-abc", "ag-abc"}} {
		if got := antigravitySessionID(tt.in); got != tt.want {
			t.Errorf("antigravitySessionID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReadTranscriptFileVariousTools(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-tools-test")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Run tasks"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Calling tools","tool_calls":[{"name":"call_mcp_tool","args":"{\"name\":\"ax_click\"}"},{"name":"replace_file_content","args":"{\"TargetFile\":\"main.go\"}"},{"name":"find_by_name","args":"{\"Pattern\":\"*.go\"}"}]}
{"step_index":2,"source":"MODEL","type":"MCP_TOOL","status":"DONE","created_at":"2026-06-12T03:22:49Z","content":"Clicked element"}
{"step_index":3,"source":"MODEL","type":"CODE_ACTION","status":"DONE","created_at":"2026-06-12T03:22:50Z","content":"Replaced content successfully"}
{"step_index":4,"source":"MODEL","type":"FIND","status":"DONE","created_at":"2026-06-12T03:22:51Z","content":"Found main.go"}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	// session_meta (0), user prompt (1), planner response (2), 3 tool results (3, 4, 5)
	if len(entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(entries))
	}

	if entries[3].ToolUseResult == nil || entries[3].ToolUseResult.Stdout != "Clicked element" {
		t.Errorf("entry 3 tool result mismatch: %+v", entries[3])
	}
	if entries[4].ToolUseResult == nil || entries[4].ToolUseResult.Stdout != "Replaced content successfully" {
		t.Errorf("entry 4 tool result mismatch: %+v", entries[4])
	}
	if entries[5].ToolUseResult == nil || entries[5].ToolUseResult.Stdout != "Found main.go" {
		t.Errorf("entry 5 tool result mismatch: %+v", entries[5])
	}
}

func TestReadTranscriptTruncatedFields(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-truncated-test")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Hello"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"truncated","truncated_fields":["thinking","tool_calls"]}
{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","status":"DONE","created_at":"2026-06-12T03:22:50Z","content":"truncated output","truncated_fields":["content"]}
`
	transcriptFullJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Hello"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Full thinking process","tool_calls":[{"name":"run_command","args":"{\"CommandLine\":\"pwd\"}"}]}
{"step_index":2,"source":"MODEL","type":"RUN_COMMAND","status":"DONE","created_at":"2026-06-12T03:22:50Z","content":"Full output content from full file"}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "transcript_full.jsonl"), []byte(transcriptFullJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	// Verify planner response received full thinking and full tool_calls
	if entries[2].Type != "assistant" || !strings.Contains(string(entries[2].Message.Content), "Full thinking process") {
		t.Errorf("assistant entry missing full thinking: %+v", entries[2])
	}

	// Verify tool result received full content
	if entries[3].ToolUseResult == nil || entries[3].ToolUseResult.Stdout != "Full output content from full file" {
		t.Errorf("tool result missing full content: %+v", entries[3])
	}
}

func TestRestoreTruncatedFieldsPreservesDuplicateOrder(t *testing.T) {
	compact := []agStep{
		{StepIndex: 7, Type: "GENERIC", CreatedAt: "2026-07-22T00:00:00Z", Content: "first compact"},
		{StepIndex: 7, Type: "GENERIC", CreatedAt: "2026-07-22T00:00:00Z", Content: "second compact", TruncatedFields: []string{"content"}},
	}
	full := map[string][]agStep{
		antigravityStepKey(compact[0]): {
			{StepIndex: 7, Type: "GENERIC", CreatedAt: "2026-07-22T00:00:00Z", Content: "first full"},
			{StepIndex: 7, Type: "GENERIC", CreatedAt: "2026-07-22T00:00:00Z", Content: "second full"},
		},
	}
	restoreTruncatedFields(compact, full)
	if got, want := compact[0].Content, "first compact"; got != want {
		t.Errorf("first content = %q, want %q", got, want)
	}
	if got, want := compact[1].Content, "second full"; got != want {
		t.Errorf("second content = %q, want %q", got, want)
	}
}

func TestReadTranscriptFallbackCWD(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-fallback-cwd")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":"Do something"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Ok","tool_calls":[{"name":"run_command","args":{"CommandLine":"\"ls\"","Cwd":"\"/Volumes/example/go/src/github.com/tmc/cc\""}}]}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(entries) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(entries))
	}

	if entries[0].CWD != "/Volumes/example/go/src/github.com/tmc/cc" {
		t.Errorf("session_meta CWD = %q, want /Volumes/example/go/src/github.com/tmc/cc", entries[0].CWD)
	}
	if entries[1].CWD != "/Volumes/example/go/src/github.com/tmc/cc" {
		t.Errorf("user entry CWD = %q, want /Volumes/example/go/src/github.com/tmc/cc", entries[1].CWD)
	}
}

func TestReadTranscriptMissingFieldsAndCorruptedJSON(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-corrupted-edge")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `

{"step_index":0}
{"corrupted json line...
{}
{"type":"USER_INPUT","content":"Valid input","created_at":"invalid-date"}
{"type":"PLANNER_RESPONSE","created_at":"2026-06-12T03:22:48Z","tool_calls":[]}
{"type":"PLANNER_RESPONSE","created_at":"2026-06-12T03:22:49Z","tool_calls":[{"name":"view_file","args":null}]}
{"type":"VIEW_FILE","content":"File contents"}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed on corrupted JSON lines: %v", err)
	}

	if len(entries) == 0 {
		t.Fatalf("expected entries, got 0")
	}
}

func TestReadTranscriptStringifiedArgsAndNestedJSON(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-stringified-args")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"type":"USER_INPUT","content":"test"}
{"step_index":1,"type":"PLANNER_RESPONSE","tool_calls":[{"name":"run_command","args":"\"{\\\"CommandLine\\\":\\\"echo hi\\\",\\\"Cwd\\\":\\\"/tmp/foo\\\"}\""},{"name":"write_to_file","args":"\"just a text string\""}]}
{"step_index":2,"type":"RUN_COMMAND","content":"hi"}
{"step_index":3,"type":"WRITE_TO_FILE","content":"done"}
`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if len(entries) < 4 {
		t.Fatalf("expected at least 4 entries, got %d", len(entries))
	}
	if entries[0].CWD != "/tmp/foo" {
		t.Errorf("CWD extracted from stringified args = %q, want /tmp/foo", entries[0].CWD)
	}
}

func TestReadTranscriptMissingWorkspaceAndLegacyFallback(t *testing.T) {
	tmp := t.TempDir()
	sessionDir := filepath.Join(tmp, "session-no-workspace")
	logDir := filepath.Join(sessionDir, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	wsContent := `{"folders":[{"path":"~/test-dir"}]}`
	if err := os.WriteFile(filepath.Join(sessionDir, "bad.code-workspace"), []byte(wsContent), 0o644); err != nil {
		t.Fatal(err)
	}

	transcriptJSONL := `{"step_index":0,"type":"USER_INPUT","content":"No CWD explicitly provided"}`
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadFile(context.Background(), sessionDir)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(entries))
	}
	home, _ := os.UserHomeDir()
	expectedPath := filepath.Join(home, "test-dir")
	if entries[0].CWD != expectedPath {
		t.Errorf("CWD with ~ expansion = %q, want %q", entries[0].CWD, expectedPath)
	}
}

func TestIsToolResultStepExclusions(t *testing.T) {
	excluded := []string{
		"SYSTEM_MESSAGE",
		"CHECKPOINT",
		"USER_INPUT",
		"PLANNER_RESPONSE",
		"CONVERSATION_HISTORY",
	}
	for _, stepType := range excluded {
		if isToolResultStep(stepType, "run_command") {
			t.Errorf("isToolResultStep(%q, %q) = true, want false", stepType, "run_command")
		}
	}
	if !isToolResultStep("ERROR_MESSAGE", "run_command") {
		t.Error("ERROR_MESSAGE must consume a pending tool call")
	}
}

func TestReadTranscriptFallbackCWDSpaces(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantCWD string
	}{
		{
			name:    "spaces",
			content: "<user_information>\n/Users/example/My Projects/cc -> tmc/cc\n</user_information>\n<USER_REQUEST>Do something</USER_REQUEST>",
			wantCWD: "/Users/example/My Projects/cc",
		},
		{
			name:    "parentheses and special chars in mapping",
			content: "<user_information>\n/Users/example/My Projects (dev) @ #1/cc -> tmc/cc\n</user_information>\n<USER_REQUEST>Do something</USER_REQUEST>",
			wantCWD: "/Users/example/My Projects (dev) @ #1/cc",
		},
		{
			name:    "working directory with parentheses",
			content: "<user_information>\nWorking directory: /Users/example/code (workspace)/app\n</user_information>",
			wantCWD: "/Users/example/code (workspace)/app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			sessionDir := filepath.Join(tmp, "session-cwd-"+strings.ReplaceAll(tt.name, " ", "-"))
			logDir := filepath.Join(sessionDir, ".system_generated", "logs")
			if err := os.MkdirAll(logDir, 0o755); err != nil {
				t.Fatal(err)
			}

			transcriptJSONL := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-12T03:22:47Z","content":%q}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-12T03:22:48Z","thinking":"Ok","tool_calls":[{"name":"run_command","args":"{\"CommandLine\":\"ls\"}"}]}
`, tt.content)
			if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(transcriptJSONL), 0o644); err != nil {
				t.Fatal(err)
			}

			entries, err := ReadFile(context.Background(), sessionDir)
			if err != nil {
				t.Fatalf("ReadFile failed: %v", err)
			}

			if len(entries) < 2 {
				t.Fatalf("expected at least 2 entries, got %d", len(entries))
			}

			if entries[0].CWD != tt.wantCWD {
				t.Errorf("session_meta CWD = %q, want %q", entries[0].CWD, tt.wantCWD)
			}
			if entries[1].CWD != tt.wantCWD {
				t.Errorf("user entry CWD = %q, want %q", entries[1].CWD, tt.wantCWD)
			}
		})
	}
}
