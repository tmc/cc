// Package codexread decodes Codex CLI rollout lines into the normalized cc
// data model. Codex sessions interleave envelope-typed lines (session_meta,
// response_item, event_msg, ...) with native Claude JSONL, so [Decode] reports
// whether a line was a Codex envelope and lets the caller fall back to a plain
// entry decode when it was not.
//
// This package is internal to cc and imports only ccmodel; cc.ReadFile and the
// streaming Reader dispatch to it.
package codexread

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/tmc/cc/internal/ccmodel"
)

// Decode decodes one Codex rollout line. isCodex reports whether the line was a
// recognized Codex envelope type; when it is false the caller should decode the
// line as a plain ccmodel.Entry. When isCodex is true, ok reports whether the
// envelope produced a transcript entry (some recognized types, e.g. reasoning,
// carry no content and yield ok=false).
func Decode(line []byte) (entry ccmodel.Entry, isCodex, ok bool) {
	var env codexEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return ccmodel.Entry{}, false, false
	}
	if !isCodexEnvelopeType(env.Type) {
		return ccmodel.Entry{}, false, false
	}
	switch env.Type {
	case "session_meta":
		e, ok := decodeCodexSessionMeta(env)
		return e, true, ok
	case "turn_context":
		e, ok := decodeCodexTurnContext(env)
		return e, true, ok
	case "response_item":
		e, ok := decodeCodexResponseItem(env)
		return e, true, ok
	case "web_search_call":
		e, ok := decodeCodexWebSearch(env)
		return e, true, ok
	case "compacted":
		return ccmodel.Entry{
			Type:      "system",
			Subtype:   "compact_boundary",
			Timestamp: env.Timestamp,
		}, true, true
	case "event_msg":
		e, ok := decodeCodexEventMsg(env)
		return e, true, ok
	default:
		// reasoning, ghost_snapshot: recognized but no transcript content.
		return ccmodel.Entry{}, true, false
	}
}

type codexEnvelope struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func isCodexEnvelopeType(t string) bool {
	switch t {
	case "session_meta", "response_item", "event_msg", "turn_context", "reasoning", "ghost_snapshot", "web_search_call", "compacted":
		return true
	default:
		return false
	}
}

func decodeCodexSessionMeta(env codexEnvelope) (ccmodel.Entry, bool) {
	// source is a plain string for top-level sessions ("cli", "vscode") but an
	// object for spawned subagent sessions; decode it as raw JSON so neither
	// shape fails the whole entry.
	var payload struct {
		ID            string          `json:"id"`
		Timestamp     string          `json:"timestamp"`
		CWD           string          `json:"cwd"`
		Originator    string          `json:"originator"`
		Source        json.RawMessage `json:"source"`
		CLIVersion    string          `json:"cli_version"`
		AgentNickname string          `json:"agent_nickname"`
		AgentRole     string          `json:"agent_role"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ccmodel.Entry{}, false
	}

	ts := env.Timestamp
	if ts.IsZero() && payload.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, payload.Timestamp); err == nil {
			ts = parsed
		}
	}

	source, spawn := codexSource(payload.Source)
	nickname := firstNonEmptyString(payload.AgentNickname, spawn.AgentNickname)
	role := firstNonEmptyString(payload.AgentRole, spawn.AgentRole)

	return ccmodel.Entry{
		Type:           "system",
		Subtype:        "session_meta",
		SessionID:      payload.ID,
		Timestamp:      ts,
		CWD:            payload.CWD,
		Version:        payload.CLIVersion,
		Originator:     payload.Originator,
		Source:         source,
		ParentThreadID: spawn.ParentThreadID,
		AgentNickname:  nickname,
		AgentRole:      role,
	}, true
}

// codexThreadSpawn is the spawn metadata carried by a codex subagent session's
// session_meta at source.subagent.thread_spawn.
type codexThreadSpawn struct {
	ParentThreadID string `json:"parent_thread_id"`
	AgentNickname  string `json:"agent_nickname"`
	AgentRole      string `json:"agent_role"`
}

// codexSource decodes the session_meta "source" field, which is a JSON string
// for top-level sessions and an object for spawned subagents. It returns the
// string form (empty when source is an object) and any thread-spawn metadata.
func codexSource(raw json.RawMessage) (source string, spawn codexThreadSpawn) {
	if len(raw) == 0 {
		return "", spawn
	}
	if err := json.Unmarshal(raw, &source); err == nil {
		return source, spawn
	}
	var obj struct {
		Subagent struct {
			ThreadSpawn codexThreadSpawn `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return "", obj.Subagent.ThreadSpawn
	}
	return "", spawn
}

func firstNonEmptyString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func decodeCodexTurnContext(env codexEnvelope) (ccmodel.Entry, bool) {
	var payload struct {
		TurnID string `json:"turn_id"`
		CWD    string `json:"cwd"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ccmodel.Entry{}, false
	}
	if payload.CWD == "" {
		return ccmodel.Entry{}, false
	}
	return ccmodel.Entry{
		Type:      "system",
		Subtype:   "turn_context",
		UUID:      payload.TurnID,
		Timestamp: env.Timestamp,
		CWD:       payload.CWD,
		IsMeta:    true,
	}, true
}

func decodeCodexResponseItem(env codexEnvelope) (ccmodel.Entry, bool) {
	var payload struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Phase     string          `json:"phase"`
		Content   json.RawMessage `json:"content"`
		Name      string          `json:"name"`
		CallID    string          `json:"call_id"`
		Arguments json.RawMessage `json:"arguments"`
		Input     json.RawMessage `json:"input"`
		Output    json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ccmodel.Entry{}, false
	}

	switch payload.Type {
	case "message":
		if payload.Role != "user" && payload.Role != "assistant" && payload.Role != "developer" {
			return ccmodel.Entry{}, false
		}
		blocks := codexTextBlocks(payload.Content)
		if len(blocks) == 0 {
			return ccmodel.Entry{}, false
		}
		content, _ := json.Marshal(blocks)
		entryType := payload.Role
		if payload.Role == "developer" {
			entryType = "system"
		}
		entry := ccmodel.Entry{
			Type:      entryType,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &ccmodel.Message{
				Role:    payload.Role,
				Content: content,
			},
		}
		if payload.Role == "developer" || payload.Role == "user" && isCodexSystemPreamble(blocks) {
			entry.IsMeta = true
		}
		return entry, true

	case "function_call", "custom_tool_call", "tool_search_call":
		toolName := payload.Name
		toolInput := payload.Input
		if payload.Type == "function_call" || payload.Type == "tool_search_call" {
			toolInput = payload.Arguments
		}
		if payload.Type == "tool_search_call" {
			toolName = "tool_search"
		}
		toolUseName := toolName
		if toolName == "exec_command" || toolName == "shell" {
			toolUseName = "Bash"
		}
		normalized := normalizeCodexToolInput(toolName, toolInput)
		content, _ := json.Marshal([]ccmodel.ContentBlock{
			{
				Type:  "tool_use",
				ID:    payload.CallID,
				Name:  toolUseName,
				Input: normalized,
			},
		})
		return ccmodel.Entry{
			Type:      "assistant",
			UUID:      payload.CallID,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &ccmodel.Message{
				ID:      payload.CallID,
				Role:    "assistant",
				Content: content,
			},
		}, true

	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		output := payload.Output
		if payload.Type == "tool_search_output" && len(output) == 0 {
			output = env.Payload
		}
		stdout, status, success, errText := parseCodexToolOutput(output)
		content, _ := json.Marshal([]ccmodel.ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: payload.CallID,
				Content:   stdout,
				IsError:   !success,
			},
		})
		res := &ccmodel.ToolUseResult{
			Type:    "tool_result",
			Stdout:  stdout,
			Status:  status,
			Success: success,
		}
		if errText != "" {
			res.Error = errText
		}
		return ccmodel.Entry{
			Type:      "user",
			UUID:      payload.CallID,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &ccmodel.Message{
				Role:    "user",
				Content: content,
			},
			ToolUseResult: res,
		}, true
	case "web_search_call":
		return decodeCodexWebSearch(env)
	case "reasoning", "ghost_snapshot":
		return ccmodel.Entry{}, false
	default:
		return ccmodel.Entry{}, false
	}
}

func decodeCodexWebSearch(env codexEnvelope) (ccmodel.Entry, bool) {
	var payload struct {
		Query  string `json:"query"`
		URL    string `json:"url"`
		Status string `json:"status"`
		Action struct {
			Type    string   `json:"type"`
			Query   string   `json:"query"`
			Queries []string `json:"queries"`
			URL     string   `json:"url"`
		} `json:"action"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ccmodel.Entry{}, false
	}
	query := payload.Query
	if query == "" {
		query = payload.Action.Query
	}
	if query == "" && len(payload.Action.Queries) > 0 {
		query = payload.Action.Queries[0]
	}
	url := payload.URL
	if url == "" {
		url = payload.Action.URL
	}
	if query == "" && url == "" {
		return ccmodel.Entry{}, false
	}
	inputFields := map[string]string{}
	if query != "" {
		inputFields["query"] = query
	}
	if url != "" {
		inputFields["url"] = url
	}
	if payload.Action.Type != "" {
		inputFields["action"] = payload.Action.Type
	}
	input, _ := json.Marshal(inputFields)
	content, _ := json.Marshal([]ccmodel.ContentBlock{
		{
			Type:  "tool_use",
			Name:  "WebSearch",
			Input: input,
		},
	})
	return ccmodel.Entry{
		Type:      "assistant",
		Timestamp: env.Timestamp,
		Message: &ccmodel.Message{
			Role:    "assistant",
			Content: content,
		},
	}, true
}

func decodeCodexEventMsg(env codexEnvelope) (ccmodel.Entry, bool) {
	var payload struct {
		Type string `json:"type"`
		Info struct {
			LastTokenUsage struct {
				InputTokens           int `json:"input_tokens"`
				CachedInputTokens     int `json:"cached_input_tokens"`
				OutputTokens          int `json:"output_tokens"`
				ReasoningOutputTokens int `json:"reasoning_output_tokens"`
				TotalTokens           int `json:"total_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return ccmodel.Entry{}, false
	}
	if payload.Type != "token_count" {
		return ccmodel.Entry{}, false
	}
	u := &ccmodel.Usage{
		InputTokens:              payload.Info.LastTokenUsage.InputTokens,
		OutputTokens:             payload.Info.LastTokenUsage.OutputTokens,
		CacheReadInputTokens:     payload.Info.LastTokenUsage.CachedInputTokens,
		CacheCreationInputTokens: 0,
	}
	if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadInputTokens == 0 {
		return ccmodel.Entry{}, false
	}
	return ccmodel.Entry{
		Type:      "system",
		Subtype:   "token_count",
		Timestamp: env.Timestamp,
		Usage:     u,
	}, true
}

// isCodexSystemPreamble detects codex user messages that contain injected
// system instructions (AGENTS.md, permissions, etc.) rather than actual user input.
func isCodexSystemPreamble(blocks []ccmodel.ContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	text := blocks[0].Text
	if len(text) > 200 {
		text = text[:200]
	}
	return strings.HasPrefix(text, "# AGENTS.md instructions for ") ||
		strings.HasPrefix(text, "<permissions instructions>") ||
		strings.HasPrefix(text, "<INSTRUCTIONS>") ||
		strings.HasPrefix(text, "<environment_context>")
}

func codexTextBlocks(raw json.RawMessage) []ccmodel.ContentBlock {
	if blocks := decodeCodexContentBlocks(raw); len(blocks) > 0 {
		return blocks
	}
	text := strings.TrimSpace(ccmodel.ExtractAnyText(raw))
	if text == "" {
		return nil
	}
	return []ccmodel.ContentBlock{{Type: "text", Text: text}}
}

func decodeCodexContentBlocks(raw json.RawMessage) []ccmodel.ContentBlock {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	blocks := make([]ccmodel.ContentBlock, 0, len(items))
	for _, item := range items {
		typ, _ := item["type"].(string)
		switch typ {
		case "input_text", "output_text", "text":
			text, _ := item["text"].(string)
			text = strings.TrimSpace(text)
			if text != "" {
				blocks = append(blocks, ccmodel.ContentBlock{Type: "text", Text: text})
			}
		case "input_image", "image", "local_image":
			block := ccmodel.ContentBlock{Type: typ}
			if s, _ := item["path"].(string); s != "" {
				block.Path = s
			}
			if s, _ := item["file_path"].(string); s != "" {
				block.FilePath = s
			}
			if s, _ := item["image_url"].(string); s != "" {
				block.ImageURL = s
			}
			if s, _ := item["url"].(string); s != "" {
				block.URL = s
			}
			if s, _ := item["data"].(string); s != "" {
				block.Data = s
			}
			if s, _ := item["mime_type"].(string); s != "" {
				block.MIMEType = s
			}
			if s, _ := item["media_type"].(string); s != "" {
				block.MediaType = s
			}
			if block.Path != "" || block.FilePath != "" || block.ImageURL != "" || block.URL != "" || block.Data != "" {
				blocks = append(blocks, block)
			}
		}
	}
	return blocks
}

func normalizeCodexToolInput(name string, raw json.RawMessage) json.RawMessage {
	args := decodeCodexArgument(raw)
	switch name {
	case "exec_command":
		command := ""
		if v, ok := args["cmd"].(string); ok {
			command = strings.TrimSpace(v)
		}
		if command == "" {
			if v, ok := args["command"].(string); ok {
				command = strings.TrimSpace(v)
			}
		}
		b, _ := json.Marshal(map[string]string{"command": command})
		return b

	case "shell":
		command := codexShellCommand(args["command"])
		if command == "" {
			if v, ok := args["command"].(string); ok {
				command = strings.TrimSpace(v)
			}
		}
		b, _ := json.Marshal(map[string]string{"command": command})
		return b
	}

	if len(args) > 0 {
		b, _ := json.Marshal(args)
		return b
	}
	var anyVal any
	if err := json.Unmarshal(raw, &anyVal); err == nil {
		b, _ := json.Marshal(map[string]any{"input": anyVal})
		return b
	}
	b, _ := json.Marshal(map[string]string{"input": string(raw)})
	return b
}

func decodeCodexArgument(raw json.RawMessage) map[string]any {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		raw = json.RawMessage(str)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}
	return nil
}

func codexShellCommand(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		parts := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) >= 3 && parts[1] == "-lc" {
			return strings.TrimSpace(parts[2])
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	default:
		return ""
	}
}

func parseCodexToolOutput(raw json.RawMessage) (stdout, status string, success bool, errText string) {
	output := decodeCodexOutputString(raw)
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", "success", true, ""
	}

	var wrapped struct {
		Output   string `json:"output"`
		Stderr   string `json:"stderr"`
		Error    string `json:"error"`
		Metadata struct {
			ExitCode int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(trimmed), &wrapped) == nil && (wrapped.Output != "" || wrapped.Stderr != "" || wrapped.Error != "" || strings.Contains(trimmed, `"exit_code"`)) {
		stdout = wrapped.Output
		if stdout == "" {
			stdout = wrapped.Stderr
		}
		if stdout == "" {
			stdout = trimmed
		}
		if wrapped.Metadata.ExitCode == 0 && wrapped.Error == "" && wrapped.Stderr == "" {
			return stdout, "success", true, ""
		}
		errText = wrapped.Error
		if errText == "" {
			errText = wrapped.Stderr
		}
		if errText == "" {
			errText = stdout
		}
		return stdout, "error", false, errText
	}

	lower := strings.ToLower(trimmed)
	if strings.Contains(lower, "error") || strings.Contains(lower, "failed") || strings.Contains(lower, "panic") {
		return trimmed, "error", false, trimmed
	}
	return trimmed, "success", true, ""
}

func decodeCodexOutputString(raw json.RawMessage) string {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str
	}
	var anyVal any
	if err := json.Unmarshal(raw, &anyVal); err == nil {
		b, _ := json.Marshal(anyVal)
		return string(b)
	}
	return string(raw)
}
