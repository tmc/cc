package cc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindSessionFilesContextCanceled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	if err := os.MkdirAll(filepath.Join(root, "claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := FindSessionFiles(ctx, time.Hour, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FindSessionFiles error = %v, want context canceled", err)
	}
}

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

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("len(entries) = %d, want 6", len(entries))
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

	var gotPrompt, gotTool, gotResult, gotWebSearch bool
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
			for _, b := range e.Message.ToolUses() {
				if b.Name != "WebSearch" {
					continue
				}
				var inp struct {
					Query string `json:"query"`
				}
				_ = json.Unmarshal(b.Input, &inp)
				if inp.Query == "golang" {
					gotWebSearch = true
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
	if !gotWebSearch {
		t.Fatalf("did not parse top-level web_search_call")
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

func TestReadFileCodexTokenCountUsage(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-02-25T10:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "sid-usage-123",
				"timestamp":   "2026-02-25T10:00:00Z",
				"cwd":         "/tmp/myproj",
				"originator":  "codex_cli_rs",
				"source":      "cli",
				"cli_version": "0.58.0",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "working"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:02Z",
			"type":      "event_msg",
			"payload": map[string]any{
				"type": "token_count",
				"info": map[string]any{
					"last_token_usage": map[string]any{
						"input_tokens":            17,
						"cached_input_tokens":     9,
						"output_tokens":           5,
						"reasoning_output_tokens": 2,
						"total_tokens":            31,
					},
				},
			},
		},
	)

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	got := entries[1]
	if got.SessionID != "sid-usage-123" {
		t.Fatalf("assistant sessionID = %q, want sid-usage-123", got.SessionID)
	}
	if got.Message == nil || got.Message.Usage == nil {
		t.Fatalf("assistant usage missing: %#v", got.Message)
	}
	if got.Message.Usage.InputTokens != 17 || got.Message.Usage.OutputTokens != 5 || got.Message.Usage.CacheReadInputTokens != 9 {
		t.Fatalf("assistant usage = %+v, want input=17 output=5 cache_read=9", got.Message.Usage)
	}
}

func TestReaderCodexTokenCountUsage(t *testing.T) {
	const jsonl = `{"timestamp":"2026-02-25T10:00:00Z","type":"session_meta","payload":{"id":"sid-usage-123","cwd":"/tmp/myproj","originator":"codex_cli_rs","source":"cli"}}
{"timestamp":"2026-02-25T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}
{"timestamp":"2026-02-25T10:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":17,"cached_input_tokens":9,"output_tokens":5,"reasoning_output_tokens":2,"total_tokens":31}}}}
{"timestamp":"2026-02-25T10:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"next"}]}}
`
	r := NewReader(context.Background(), strings.NewReader(jsonl))

	var entries []Entry
	for r.Next() {
		entries = append(entries, r.Entry())
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Reader err: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(entries))
	}
	if entries[1].Message == nil || entries[1].Message.Usage == nil {
		t.Fatalf("assistant usage missing: %#v", entries[1].Message)
	}
	if entries[1].Message.Usage.InputTokens != 17 || entries[1].Message.Usage.OutputTokens != 5 || entries[1].Message.Usage.CacheReadInputTokens != 9 {
		t.Fatalf("assistant usage = %+v, want input=17 output=5 cache_read=9", entries[1].Message.Usage)
	}
	if entries[2].SessionID != "sid-usage-123" {
		t.Fatalf("user sessionID = %q, want sid-usage-123", entries[2].SessionID)
	}
}

func TestReadFileCodexTurnContextCWD(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	const cwd = "/Users/tmc/go/src/github.com/tmc/appledocs"

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-19T03:47:27.046Z",
			"type":      "turn_context",
			"payload": map[string]any{
				"turn_id": "019e3e58-7c8f-7e20-8e66-9642f7fa292a",
				"cwd":     cwd,
			},
		},
		map[string]any{
			"timestamp": "2026-05-19T03:47:28.000Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "he AppKit errors are two separate generic problems"},
				},
			},
		},
	)

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Subtype != "turn_context" {
		t.Fatalf("subtype = %q, want turn_context", entries[0].Subtype)
	}
	if entries[0].CWD != cwd {
		t.Fatalf("cwd = %q, want %q", entries[0].CWD, cwd)
	}

	sum := Summarize(path, entries)
	if sum.CWD != cwd {
		t.Fatalf("summary cwd = %q, want %q", sum.CWD, cwd)
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

	entries, err := ReadFile(context.Background(), path)
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

	entries, err := ReadFile(context.Background(), path)
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

func TestReadFileCodexWebSearchResponseItem(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-05T01:28:25Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":   "web_search_call",
				"status": "completed",
				"action": map[string]any{
					"type": "search",
					"queries": []string{
						"Apple Developer VZVirtualMachine attachStorageDevice",
						"VZVirtioFileSystemDeviceConfiguration hot add",
					},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-05T01:28:47Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":   "web_search_call",
				"status": "completed",
				"action": map[string]any{
					"type": "open_page",
					"url":  "https://developer.apple.com/documentation/virtualization/vzvirtualmachine",
				},
			},
		},
	)

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	var gotQuery, gotURL bool
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		for _, b := range e.Message.ToolUses() {
			if b.Name != "WebSearch" {
				continue
			}
			var inp struct {
				Query  string `json:"query"`
				URL    string `json:"url"`
				Action string `json:"action"`
			}
			_ = json.Unmarshal(b.Input, &inp)
			if inp.Action == "search" && strings.Contains(inp.Query, "VZVirtualMachine") {
				gotQuery = true
			}
			if inp.Action == "open_page" && strings.Contains(inp.URL, "vzvirtualmachine") {
				gotURL = true
			}
		}
	}
	if !gotQuery {
		t.Fatalf("did not parse response_item web search query")
	}
	if !gotURL {
		t.Fatalf("did not parse response_item web search URL")
	}
}

func TestReadFileCodexToolSearch(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-01T16:50:57Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "tool_search_call",
				"call_id": "call-search",
				"status":  "completed",
				"arguments": map[string]any{
					"query": "browser navigate screenshot local url",
					"limit": 5,
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-01T16:50:58Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "tool_search_output",
				"call_id": "call-search",
				"status":  "completed",
				"tools": []map[string]any{
					{"type": "namespace", "name": "mcp__browser__"},
				},
			},
		},
	)

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	use := entries[0]
	if use.Type != "assistant" || use.UUID != "call-search" || use.Message == nil {
		t.Fatalf("tool search use entry = %#v", use)
	}
	uses := use.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "tool_search" {
		t.Fatalf("tool uses = %#v, want tool_search", uses)
	}
	if !strings.Contains(string(uses[0].Input), "browser navigate") {
		t.Fatalf("tool search input = %s", uses[0].Input)
	}

	result := entries[1]
	if result.Type != "user" || result.UUID != "call-search" || result.Message == nil || result.ToolUseResult == nil {
		t.Fatalf("tool search result entry = %#v", result)
	}
	results := result.Message.ToolResults()
	if len(results) != 1 || results[0].ToolUseID != "call-search" {
		t.Fatalf("tool results = %#v", results)
	}
	if !strings.Contains(result.ToolUseResult.Stdout, "mcp__browser__") {
		t.Fatalf("tool search stdout = %q", result.ToolUseResult.Stdout)
	}
}

func TestReadFileCodexSubagentSessionMeta(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")

	// A spawned codex subagent's session_meta carries source as an object
	// (source.subagent.thread_spawn) rather than a plain string.
	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-25T05:31:12Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "019e5d9d-child",
				"cwd":         "/work/apple",
				"originator":  "codex-tui",
				"cli_version": "0.128.0",
				"source": map[string]any{
					"subagent": map[string]any{
						"thread_spawn": map[string]any{
							"parent_thread_id": "019e5385-parent",
							"depth":            1,
							"agent_nickname":   "Peirce",
							"agent_role":       "worker",
						},
					},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-25T05:31:13Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "do the work"},
				},
			},
		},
	)

	entries, err := ReadFile(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (session_meta must not be dropped)", len(entries))
	}
	meta := entries[0]
	if meta.Subtype != "session_meta" {
		t.Fatalf("subtype = %q, want session_meta", meta.Subtype)
	}
	if meta.SessionID != "019e5d9d-child" {
		t.Fatalf("session id = %q, want 019e5d9d-child", meta.SessionID)
	}
	if meta.CWD != "/work/apple" {
		t.Fatalf("cwd = %q, want /work/apple", meta.CWD)
	}
	if meta.Source != "" {
		t.Fatalf("object source should yield empty Source string, got %q", meta.Source)
	}
	if meta.ParentThreadID != "019e5385-parent" {
		t.Fatalf("parent thread id = %q, want 019e5385-parent", meta.ParentThreadID)
	}
	if meta.AgentNickname != "Peirce" || meta.AgentRole != "worker" {
		t.Fatalf("agent nickname/role = %q/%q, want Peirce/worker", meta.AgentNickname, meta.AgentRole)
	}

	// String-form source (top-level session) still decodes and carries no spawn.
	sum := Summarize(path, entries)
	if sum.SessionID != "019e5d9d-child" {
		t.Fatalf("summary session id = %q, want 019e5d9d-child", sum.SessionID)
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

	got, err := FindSessionFiles(context.Background(), 24*time.Hour, "github.com/tmc/cc")
	if err != nil {
		t.Fatalf("FindSessionFiles: %v", err)
	}
	if !containsPath(got, codexFile) {
		t.Fatalf("FindSessionFiles did not include codex file; got=%v", got)
	}

	got, err = FindSessionFiles(context.Background(), 24*time.Hour, "does-not-match-project")
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
