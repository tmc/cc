package cc

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reader reads entries from a JSONL session file.
type Reader struct {
	scanner *bufio.Scanner
	err     error
	entry   Entry
}

// NewReader creates a Reader from an io.Reader.
func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 256*1024), 10*1024*1024)
	return &Reader{scanner: s}
}

// Next advances to the next entry. Returns false at EOF or on error.
func (r *Reader) Next() bool {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			continue
		}
		entry, ok := decodeEntryLine([]byte(line))
		if !ok {
			continue
		}
		r.entry = entry
		return true
	}
	r.err = r.scanner.Err()
	return false
}

type codexEnvelope struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func decodeEntryLine(line []byte) (Entry, bool) {
	var env codexEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Entry{}, false
	}
	if !isCodexEnvelopeType(env.Type) {
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return Entry{}, false
		}
		return entry, true
	}

	switch env.Type {
	case "session_meta":
		return decodeCodexSessionMeta(env)
	case "response_item":
		return decodeCodexResponseItem(env)
	case "compacted":
		return Entry{
			Type:      "system",
			Subtype:   "compact_boundary",
			Timestamp: env.Timestamp,
		}, true
	case "event_msg", "turn_context", "reasoning", "ghost_snapshot", "web_search_call":
		return Entry{}, false
	default:
		return Entry{}, false
	}
}

func isCodexEnvelopeType(t string) bool {
	switch t {
	case "session_meta", "response_item", "event_msg", "turn_context", "reasoning", "ghost_snapshot", "web_search_call", "compacted":
		return true
	default:
		return false
	}
}

func decodeCodexSessionMeta(env codexEnvelope) (Entry, bool) {
	var payload struct {
		ID         string `json:"id"`
		Timestamp  string `json:"timestamp"`
		CWD        string `json:"cwd"`
		Originator string `json:"originator"`
		Source     string `json:"source"`
		CLIVersion string `json:"cli_version"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return Entry{}, false
	}

	ts := env.Timestamp
	if ts.IsZero() && payload.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, payload.Timestamp); err == nil {
			ts = parsed
		}
	}

	return Entry{
		Type:       "system",
		Subtype:    "session_meta",
		SessionID:  payload.ID,
		Timestamp:  ts,
		CWD:        payload.CWD,
		Version:    payload.CLIVersion,
		Originator: payload.Originator,
		Source:     payload.Source,
	}, true
}

func decodeCodexResponseItem(env codexEnvelope) (Entry, bool) {
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
		return Entry{}, false
	}

	switch payload.Type {
	case "message":
		if payload.Role != "user" && payload.Role != "assistant" {
			return Entry{}, false
		}
		blocks := codexTextBlocks(payload.Content)
		if len(blocks) == 0 {
			return Entry{}, false
		}
		content, _ := json.Marshal(blocks)
		entry := Entry{
			Type:      payload.Role,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &Message{
				Role:    payload.Role,
				Content: content,
			},
		}
		if payload.Role == "user" && isCodexSystemPreamble(blocks) {
			entry.IsMeta = true
		}
		return entry, true

	case "function_call", "custom_tool_call":
		toolName := payload.Name
		toolInput := payload.Input
		if payload.Type == "function_call" {
			toolInput = payload.Arguments
		}
		toolUseName := toolName
		if toolName == "exec_command" || toolName == "shell" {
			toolUseName = "Bash"
		}
		normalized := normalizeCodexToolInput(toolName, toolInput)
		content, _ := json.Marshal([]ContentBlock{
			{
				Type:  "tool_use",
				ID:    payload.CallID,
				Name:  toolUseName,
				Input: normalized,
			},
		})
		return Entry{
			Type:      "assistant",
			UUID:      payload.CallID,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &Message{
				ID:      payload.CallID,
				Role:    "assistant",
				Content: content,
			},
		}, true

	case "function_call_output", "custom_tool_call_output":
		stdout, status, success, errText := parseCodexToolOutput(payload.Output)
		content, _ := json.Marshal([]ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: payload.CallID,
				Content:   stdout,
				IsError:   !success,
			},
		})
		res := &ToolUseResult{
			Type:    "tool_result",
			Stdout:  stdout,
			Status:  status,
			Success: success,
		}
		if errText != "" {
			res.Error = errText
		}
		return Entry{
			Type:      "user",
			UUID:      payload.CallID,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &Message{
				Role:    "user",
				Content: content,
			},
			ToolUseResult: res,
		}, true
	case "web_search_call":
		return decodeCodexWebSearch(env)
	case "reasoning", "ghost_snapshot":
		return Entry{}, false
	default:
		return Entry{}, false
	}
}

func decodeCodexWebSearch(env codexEnvelope) (Entry, bool) {
	var payload struct {
		Status string `json:"status"`
		Action struct {
			Type    string   `json:"type"`
			Query   string   `json:"query"`
			Queries []string `json:"queries"`
		} `json:"action"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return Entry{}, false
	}
	query := payload.Action.Query
	if query == "" && len(payload.Action.Queries) > 0 {
		query = payload.Action.Queries[0]
	}
	if query == "" {
		return Entry{}, false
	}
	input, _ := json.Marshal(map[string]string{"query": query})
	content, _ := json.Marshal([]ContentBlock{
		{
			Type:  "tool_use",
			Name:  "WebSearch",
			Input: input,
		},
	})
	return Entry{
		Type:      "assistant",
		Timestamp: env.Timestamp,
		Message: &Message{
			Role:    "assistant",
			Content: content,
		},
	}, true
}

// isCodexSystemPreamble detects codex user messages that contain injected
// system instructions (AGENTS.md, permissions, etc.) rather than actual user input.
func isCodexSystemPreamble(blocks []ContentBlock) bool {
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

func codexTextBlocks(raw json.RawMessage) []ContentBlock {
	var items []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &items); err == nil {
		blocks := make([]ContentBlock, 0, len(items))
		for _, item := range items {
			text := strings.TrimSpace(item.Text)
			if text == "" {
				continue
			}
			blocks = append(blocks, ContentBlock{Type: "text", Text: text})
		}
		if len(blocks) > 0 {
			return blocks
		}
	}
	text := strings.TrimSpace(ExtractAnyText(raw))
	if text == "" {
		return nil
	}
	return []ContentBlock{{Type: "text", Text: text}}
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

// Entry returns the current entry.
func (r *Reader) Entry() Entry { return r.entry }

// Err returns any error from scanning.
func (r *Reader) Err() error { return r.err }

// ReadAll reads all entries from the reader.
func ReadAll(r io.Reader) ([]Entry, error) {
	rd := NewReader(r)
	var entries []Entry
	for rd.Next() {
		entries = append(entries, rd.Entry())
	}
	return entries, rd.Err()
}

// ReadFile reads all entries from a JSONL file.
func ReadFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAll(f)
}

// SessionSummary holds summarized metadata for a session file.
type SessionSummary struct {
	SessionID    string    `json:"session_id"`
	File         string    `json:"file"`
	Project      string    `json:"project"`
	CWD          string    `json:"cwd,omitempty"`
	GitBranch    string    `json:"git_branch,omitempty"`
	Version      string    `json:"version,omitempty"`
	Slug         string    `json:"slug,omitempty"`
	Model        string    `json:"model,omitempty"`
	FirstTime    time.Time `json:"first_time"`
	LastTime     time.Time `json:"last_time"`
	UserMessages int       `json:"user_messages"`
	AsstMessages int       `json:"asst_messages"`
	ToolUses     int       `json:"tool_uses"`
	TotalLines   int       `json:"total_lines"`
	Compactions  int       `json:"compactions,omitempty"`
	FirstPrompt  string    `json:"first_prompt,omitempty"`
	CustomTitle  string    `json:"custom_title,omitempty"`
}

// Summarize builds a SessionSummary from entries.
func Summarize(file string, entries []Entry) SessionSummary {
	s := SessionSummary{File: file}
	for _, e := range entries {
		s.TotalLines++
		if e.SessionID != "" && s.SessionID == "" {
			s.SessionID = e.SessionID
		}
		if e.Version != "" && s.Version == "" {
			s.Version = e.Version
		}
		if e.CWD != "" && s.CWD == "" {
			s.CWD = e.CWD
		}
		if e.GitBranch != "" && s.GitBranch == "" {
			s.GitBranch = e.GitBranch
		}
		if e.Slug != "" && s.Slug == "" {
			s.Slug = e.Slug
		}
		if !e.Timestamp.IsZero() {
			if s.FirstTime.IsZero() {
				s.FirstTime = e.Timestamp
			}
			s.LastTime = e.Timestamp
		}
		if e.Type == "custom-title" && e.CustomTitle != "" {
			s.CustomTitle = e.CustomTitle
		}
		if e.Type == "system" && e.Subtype == "compact_boundary" {
			s.Compactions++
		}
		if e.Message != nil && !e.IsCompactSummary {
			switch e.Message.Role {
			case "user":
				s.UserMessages++
				if s.FirstPrompt == "" && !e.IsMeta {
					s.FirstPrompt = ExtractText(e.Message.Content)
				}
				if s.Model == "" && e.Message.Model != "" {
					s.Model = e.Message.Model
				}
			case "assistant":
				s.AsstMessages++
				if s.Model == "" && e.Message.Model != "" {
					s.Model = e.Message.Model
				}
				// Count tool uses.
				var blocks []ContentBlock
				if json.Unmarshal(e.Message.Content, &blocks) == nil {
					for _, b := range blocks {
						if b.Type == "tool_use" {
							s.ToolUses++
						}
					}
				}
			}
		}
	}
	return s
}

// ExtractText pulls the first text content from a message content field.
func ExtractText(raw json.RawMessage) string {
	return collapseWhitespace(ExtractAnyText(raw), 200)
}

func collapseWhitespace(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// FindSessionFiles finds JSONL session files under ~/.claude/projects/,
// ~/.gemini/projects/, and ~/.codex/sessions/.
// It excludes subagent files and filters by modification time.
func FindSessionFiles(since time.Duration, project string) ([]string, error) {
	ch, err := ClaudeHome()
	if err != nil {
		return nil, err
	}
	gh, _ := GeminiHome()
	xh, _ := CodexHome()

	cutoff := time.Now().Add(-since)
	var files []string

	type rootDir struct {
		path string
		kind string
	}

	dirs := []rootDir{{path: filepath.Join(ch, "projects"), kind: "claude"}}
	if gh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(gh, "projects"), kind: "gemini"})
	}
	if xh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(xh, "sessions"), kind: "codex"})
	}

	for _, dir := range dirs {
		filepath.Walk(dir.path, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() && info.Name() == "subagents" {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				return nil
			}
			if project != "" {
				q := strings.ToLower(project)
				switch dir.kind {
				case "codex":
					if !codexPathMatchesProject(path, q) {
						return nil
					}
				default:
					rel, _ := filepath.Rel(dir.path, path)
					if !strings.Contains(strings.ToLower(rel), q) {
						return nil
					}
				}
			}
			files = append(files, path)
			return nil
		})
	}
	return files, nil
}

func codexPathMatchesProject(path, query string) bool {
	entries, err := ReadFile(path)
	if err != nil {
		return strings.Contains(strings.ToLower(path), query)
	}
	for _, e := range entries {
		if e.CWD != "" && strings.Contains(strings.ToLower(e.CWD), query) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(path), query)
}
