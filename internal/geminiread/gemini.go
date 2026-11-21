// Package geminiread decodes Gemini CLI JSON session files
// (~/.gemini/tmp/<project>/chats/session-*.json) into the normalized cc data model.
package geminiread

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/ccmodel"
)

// IsSessionPath reports whether path looks like a Gemini CLI chat session file
// (.../chats/session-*.json).
func IsSessionPath(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "session-") || !strings.HasSuffix(base, ".json") {
		return false
	}
	return filepath.Base(filepath.Dir(path)) == "chats"
}

type chatFile struct {
	SessionID   string        `json:"sessionId"`
	ProjectHash string        `json:"projectHash"`
	StartTime   string        `json:"startTime"`
	LastUpdated string        `json:"lastUpdated"`
	Messages    []chatMessage `json:"messages"`
}

type chatMessage struct {
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Model     string          `json:"model"`
	Thoughts  []chatThought   `json:"thoughts"`
	ToolCalls []chatToolCall  `json:"toolCalls"`
	Tokens    struct {
		Input    int `json:"input"`
		Output   int `json:"output"`
		Cached   int `json:"cached"`
		Thoughts int `json:"thoughts"`
	} `json:"tokens"`
}

type chatThought struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	Timestamp   string `json:"timestamp"`
}

type chatToolCall struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args"`
	Result json.RawMessage `json:"result"`
	Status string          `json:"status"`
}

// ReadFile reads and normalizes a Gemini CLI JSON session file.
func ReadFile(ctx context.Context, path string) ([]ccmodel.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sess chatFile
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, fmt.Errorf("geminiread: decode %s: %w", path, err)
	}
	if sess.SessionID == "" && len(sess.Messages) == 0 {
		return nil, fmt.Errorf("geminiread: empty session %s", path)
	}

	cwd := workspaceFromPath(path, sess.ProjectHash)
	sid := sess.SessionID
	if sid == "" {
		sid = strings.TrimSuffix(filepath.Base(path), ".json")
	}

	var entries []ccmodel.Entry
	startTime := parseTime(sess.StartTime)
	if !startTime.IsZero() || cwd != "" {
		entries = append(entries, ccmodel.Entry{
			Type:      "session_meta",
			Subtype:   "session_meta",
			SessionID: sid,
			Timestamp: startTime,
			CWD:       cwd,
			Source:    "gemini",
		})
	}

	for _, m := range sess.Messages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ts := parseTime(m.Timestamp)
		switch m.Type {
		case "user":
			text := extractContentText(m.Content)
			if text == "" {
				continue
			}
			msgContent, _ := json.Marshal([]ccmodel.ContentBlock{{Type: "text", Text: text}})
			entries = append(entries, ccmodel.Entry{
				Type:      "user",
				SessionID: sid,
				UUID:      m.ID,
				Timestamp: ts,
				CWD:       cwd,
				Source:    "gemini",
				Message: &ccmodel.Message{
					ID:      m.ID,
					Role:    "user",
					Content: msgContent,
				},
			})

		case "gemini":
			var blocks []ccmodel.ContentBlock
			for _, th := range m.Thoughts {
				tText := strings.TrimSpace(th.Description)
				if th.Subject != "" && tText != "" {
					tText = th.Subject + ": " + tText
				} else if th.Subject != "" {
					tText = th.Subject
				}
				if tText != "" {
					blocks = append(blocks, ccmodel.ContentBlock{
						Type: "thinking",
						Text: tText,
					})
				}
			}
			if text := extractContentText(m.Content); text != "" {
				blocks = append(blocks, ccmodel.ContentBlock{
					Type: "text",
					Text: text,
				})
			}
			var resultBlocks []ccmodel.ContentBlock
			for i, tc := range m.ToolCalls {
				tcID := tc.ID
				if tcID == "" {
					tcID = fmt.Sprintf("gemini-tc-%d", i)
				}
				blocks = append(blocks, ccmodel.ContentBlock{
					Type:  "tool_use",
					ID:    tcID,
					Name:  normalizeToolName(tc.Name),
					Input: tc.Args,
				})
				if len(tc.Result) > 0 {
					resultBlocks = append(resultBlocks, ccmodel.ContentBlock{
						Type:      "tool_result",
						ToolUseID: tcID,
						Content:   extractToolResultText(tc.Result),
						IsError:   tc.Status == "error",
					})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			msgContent, _ := json.Marshal(blocks)
			var usage *ccmodel.Usage
			if m.Tokens.Input > 0 || m.Tokens.Output > 0 || m.Tokens.Cached > 0 || m.Tokens.Thoughts > 0 {
				usage = &ccmodel.Usage{
					InputTokens:          m.Tokens.Input,
					OutputTokens:         m.Tokens.Output + m.Tokens.Thoughts,
					CacheReadInputTokens: m.Tokens.Cached,
				}
			}
			entries = append(entries, ccmodel.Entry{
				Type:      "assistant",
				SessionID: sid,
				UUID:      m.ID,
				Timestamp: ts,
				CWD:       cwd,
				Source:    "gemini",
				Message: &ccmodel.Message{
					ID:      m.ID,
					Role:    "assistant",
					Content: msgContent,
					Model:   m.Model,
					Usage:   usage,
				},
			})
			if len(resultBlocks) > 0 {
				resContent, _ := json.Marshal(resultBlocks)
				entries = append(entries, ccmodel.Entry{
					Type:      "user",
					SessionID: sid,
					Timestamp: ts,
					CWD:       cwd,
					Source:    "gemini",
					Message: &ccmodel.Message{
						Role:    "user",
						Content: resContent,
					},
				})
			}
		}
	}
	return entries, nil
}

func normalizeToolName(name string) string {
	switch strings.ToLower(name) {
	case "run_shell_command", "run_command":
		return "Bash"
	case "read_file":
		return "View"
	case "write_file":
		return "Write"
	case "replace", "edit_file":
		return "Edit"
	case "list_directory", "list_dir":
		return "LS"
	case "search_file_content", "grep_search":
		return "Grep"
	case "glob":
		return "Glob"
	case "google_web_search", "web_search":
		return "WebSearch"
	case "web_fetch":
		return "WebFetch"
	default:
		return name
	}
}

func extractContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	type part struct {
		Text string `json:"text"`
	}
	var parts []part
	if json.Unmarshal(raw, &parts) != nil {
		var content struct {
			Parts []part `json:"parts"`
		}
		if json.Unmarshal(raw, &content) != nil {
			return ""
		}
		parts = content.Parts
	}
	var texts []string
	for _, part := range parts {
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func extractToolResultText(raw json.RawMessage) string {
	var items []struct {
		FunctionResponse *struct {
			Response json.RawMessage `json:"response"`
		} `json:"functionResponse"`
	}
	if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
		var parts []string
		for _, item := range items {
			if item.FunctionResponse != nil && len(item.FunctionResponse.Response) > 0 {
				var m map[string]any
				if json.Unmarshal(item.FunctionResponse.Response, &m) == nil {
					if out, ok := m["output"].(string); ok && out != "" {
						parts = append(parts, out)
						continue
					}
					if errStr, ok := m["error"].(string); ok && errStr != "" {
						parts = append(parts, errStr)
						continue
					}
				}
				parts = append(parts, string(item.FunctionResponse.Response))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return string(raw)
}

func workspaceFromPath(path, projectHash string) string {
	projectDir := filepath.Dir(filepath.Dir(path))
	if b, err := os.ReadFile(filepath.Join(projectDir, ".project_root")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	gh, err := ccpaths.GeminiHome()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(gh, "projects.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Projects map[string]string `json:"projects"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	if projectHash != "" {
		for p := range cfg.Projects {
			h := sha256.Sum256([]byte(p))
			if hex.EncodeToString(h[:]) == projectHash {
				return p
			}
		}
	}

	base := filepath.Base(projectDir)
	var match string
	for p, name := range cfg.Projects {
		if p == "" || name != base {
			continue
		}
		if match != "" {
			return ""
		}
		match = p
	}
	return match
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t.UTC()
}
