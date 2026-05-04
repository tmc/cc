package cc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadFileCodexCLI(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-02-25T10:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "sid-cli-123",
				"timestamp":   "2026-02-25T10:00:00Z",
				"cwd":         "/tmp/myproj",
				"originator":  "codex_cli_rs",
				"source":      "cli",
				"cli_version": "0.58.0",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:00Z",
			"type":      "web_search_call",
			"payload":   map[string]any{"query": "golang"},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "hello from codex"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":  "reasoning",
				"phase": "analysis",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "shell",
				"call_id":   "call-1",
				"arguments": `{"command":["bash","-lc","go test ./..."]}`,
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:03Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-1",
				"output":  `{"output":"ok","metadata":{"exit_code":0}}`,
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:04Z",
			"type":      "compacted",
			"payload":   map[string]any{"message": ""},
		},
	)

	entries, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5", len(entries))
	}
	if entries[0].Subtype != "session_meta" {
		t.Fatalf("meta subtype = %q, want session_meta", entries[0].Subtype)
	}
	if entries[0].SessionID != "sid-cli-123" {
		t.Fatalf("meta sessionID = %q, want sid-cli-123", entries[0].SessionID)
	}
	if entries[0].CWD != "/tmp/myproj" {
		t.Fatalf("meta cwd = %q, want /tmp/myproj", entries[0].CWD)
	}
	if entries[0].Originator != "codex_cli_rs" || entries[0].Source != "cli" {
		t.Fatalf("meta origin/source = %q/%q", entries[0].Originator, entries[0].Source)
	}

	var gotPrompt, gotTool, gotResult bool
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "user" && e.Message.TextContent() == "hello from codex" {
			gotPrompt = true
		}
		if e.Message != nil && e.Message.Role == "assistant" {
			for _, b := range e.Message.ToolUses() {
				if b.Name != "Bash" {
					continue
				}
				var inp struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(b.Input, &inp)
				if inp.Command == "go test ./..." {
					gotTool = true
				}
			}
		}
		if e.ToolUseResult != nil && e.ToolUseResult.Stdout == "ok" && e.ToolUseResult.Success {
			gotResult = true
		}
	}
	if !gotPrompt {
		t.Fatalf("did not parse user prompt")
	}
	if !gotTool {
		t.Fatalf("did not parse shell tool call as Bash")
	}
	if !gotResult {
		t.Fatalf("did not parse function_call_output")
	}

	sum := Summarize(path, entries)
	if sum.SessionID != "sid-cli-123" {
		t.Fatalf("summary session id = %q, want sid-cli-123", sum.SessionID)
	}
	if sum.CWD != "/tmp/myproj" {
		t.Fatalf("summary cwd = %q, want /tmp/myproj", sum.CWD)
	}
	if sum.FirstPrompt != "hello from codex" {
		t.Fatalf("summary first prompt = %q, want hello from codex", sum.FirstPrompt)
	}
	if sum.Compactions != 1 {
		t.Fatalf("summary compactions = %d, want 1", sum.Compactions)
	}
}

func TestReadFileCodexDesktopExecCommand(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-02-25T11:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "sid-app-123",
				"cwd":         "/work/app",
				"originator":  "Codex Desktop",
				"source":      "vscode",
				"cli_version": "0.104.0-alpha.1",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T11:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":  "message",
				"role":  "assistant",
				"phase": "commentary",
				"content": []map[string]any{
					{"type": "output_text", "text": "working"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T11:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "exec_command",
				"call_id":   "call-2",
				"arguments": `{"cmd":"git status"}`,
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T11:00:03Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-2",
				"output":  "apply_patch verification failed: boom",
			},
		},
	)

	entries, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var gotAssistant, gotCommand, gotError bool
	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "assistant" && e.Message.TextContent() == "working" && e.Phase == "commentary" {
			gotAssistant = true
		}
		if e.Message != nil && e.Message.Role == "assistant" {
			for _, b := range e.Message.ToolUses() {
				if b.Name != "Bash" {
					continue
				}
				var inp struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(b.Input, &inp)
				if inp.Command == "git status" {
					gotCommand = true
				}
			}
		}
		if e.ToolUseResult != nil && !e.ToolUseResult.Success && strings.Contains(strings.ToLower(e.ToolUseResult.Error), "failed") {
			gotError = true
		}
	}

	if !gotAssistant {
		t.Fatalf("did not parse assistant message")
	}
	if !gotCommand {
		t.Fatalf("did not parse exec_command as Bash")
	}
	if !gotError {
		t.Fatalf("did not parse plain-string tool output error")
	}
}

func TestReadFileCodexGoalMode(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-03T12:45:09Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "019deddc-b7dc-75a2-a393-52ded8ebe04a",
				"cwd":         "/work/repo",
				"originator":  "codex-tui",
				"source":      "cli",
				"cli_version": "0.128.0",
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:10Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "Continue working toward the active thread goal.\n\n<untrusted_objective>\nship it\n</untrusted_objective>"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:11Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "what remains?"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:12Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "get_goal",
				"call_id":   "call-goal",
				"arguments": `{}`,
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:13Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-goal",
				"output":  `{"goal":{"status":"active"}}`,
			},
		},
	)

	entries, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5", len(entries))
	}

	dev := entries[1]
	if dev.Type != "system" || dev.Message == nil || dev.Message.Role != "developer" {
		t.Fatalf("developer entry = type %q role %v, want system/developer", dev.Type, roleOf(dev.Message))
	}
	if !dev.IsMeta {
		t.Fatalf("developer goal message should be meta")
	}
	if !strings.Contains(dev.Message.TextContent(), "ship it") {
		t.Fatalf("developer goal text not preserved: %q", dev.Message.TextContent())
	}

	toolResult := entries[4]
	if toolResult.Message == nil || !toolResult.Message.IsToolResultOnly() {
		t.Fatalf("tool result should be tool-result-only: %#v", toolResult)
	}

	sum := Summarize(path, entries)
	if sum.UserMessages != 1 {
		t.Fatalf("summary user messages = %d, want 1", sum.UserMessages)
	}
	if sum.AsstMessages != 1 {
		t.Fatalf("summary assistant messages = %d, want 1", sum.AsstMessages)
	}
	if sum.FirstPrompt != "what remains?" {
		t.Fatalf("summary first prompt = %q, want what remains?", sum.FirstPrompt)
	}
	if sum.ToolUses != 1 {
		t.Fatalf("summary tool uses = %d, want 1", sum.ToolUses)
	}
}

func roleOf(m *Message) string {
	if m == nil {
		return ""
	}
	return m.Role
}

func TestFindSessionFilesIncludesCodexAndProjectFilterByCWD(t *testing.T) {
	tmp := t.TempDir()
	claudeHome := filepath.Join(tmp, ".claude")
	geminiHome := filepath.Join(tmp, ".gemini")
	codexHome := filepath.Join(tmp, ".codex")

	for _, dir := range []string{
		filepath.Join(claudeHome, "projects"),
		filepath.Join(geminiHome, "projects"),
		filepath.Join(codexHome, "sessions", "2026", "02", "25"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}

	t.Setenv("CLAUDE_HOME", claudeHome)
	t.Setenv("GEMINI_HOME", geminiHome)
	t.Setenv("CODEX_HOME", codexHome)

	codexFile := filepath.Join(codexHome, "sessions", "2026", "02", "25", "rollout-2026-02-25T12-00-00-abc.jsonl")
	writeJSONL(t, codexFile,
		map[string]any{
			"timestamp": "2026-02-25T12:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "sid-codex-proj",
				"cwd":         "/Volumes/tmc/go/src/github.com/tmc/cc",
				"originator":  "Codex Desktop",
				"source":      "vscode",
				"cli_version": "0.104.0-alpha.1",
			},
		},
	)

	got, err := FindSessionFiles(24*time.Hour, "github.com/tmc/cc")
	if err != nil {
		t.Fatalf("FindSessionFiles: %v", err)
	}
	if !containsPath(got, codexFile) {
		t.Fatalf("FindSessionFiles did not include codex file; got=%v", got)
	}

	got, err = FindSessionFiles(24*time.Hour, "does-not-match-project")
	if err != nil {
		t.Fatalf("FindSessionFiles (project mismatch): %v", err)
	}
	if containsPath(got, codexFile) {
		t.Fatalf("FindSessionFiles should not include codex file for mismatched project; got=%v", got)
	}
}

func writeJSONL(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s): %v", path, err)
	}
	defer f.Close()
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
