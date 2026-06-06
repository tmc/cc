package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type openCodeSessionFile struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug"`
	Version   string          `json:"version"`
	ProjectID string          `json:"projectID"`
	Directory string          `json:"directory"`
	Title     string          `json:"title"`
	Time      openCodeTime    `json:"time"`
	Summary   openCodeSummary `json:"summary"`
}

type openCodeSummary struct {
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Files     int `json:"files"`
}

type openCodeMessageFile struct {
	ID         string             `json:"id"`
	SessionID  string             `json:"sessionID"`
	Role       string             `json:"role"`
	Time       openCodeTime       `json:"time"`
	ParentID   string             `json:"parentID"`
	ModelID    string             `json:"modelID"`
	ProviderID string             `json:"providerID"`
	Mode       string             `json:"mode"`
	Agent      string             `json:"agent"`
	Path       openCodePath       `json:"path"`
	Tokens     openCodeTokens     `json:"tokens"`
	Summary    openCodeMsgSummary `json:"summary"`
	Model      openCodeModel      `json:"model"`
}

type openCodeMsgSummary struct {
	Title string `json:"title"`
}

type openCodeModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

type openCodePath struct {
	CWD  string `json:"cwd"`
	Root string `json:"root"`
}

type openCodeTime struct {
	Created   int64 `json:"created"`
	Updated   int64 `json:"updated"`
	Completed int64 `json:"completed"`
}

type openCodeTokens struct {
	Input  int                `json:"input"`
	Output int                `json:"output"`
	Cache  openCodeTokenCache `json:"cache"`
}

type openCodeTokenCache struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type openCodePartFile struct {
	ID        string         `json:"id"`
	SessionID string         `json:"sessionID"`
	MessageID string         `json:"messageID"`
	Type      string         `json:"type"`
	CallID    string         `json:"callID"`
	Text      string         `json:"text"`
	Tool      string         `json:"tool"`
	State     map[string]any `json:"state"`
	Time      openCodeTime   `json:"time"`
}

func isOpenCodeSessionPath(path string) bool {
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "ses_") || !strings.HasSuffix(base, ".json") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "storage" && parts[i+1] == "session" {
			return true
		}
	}
	return false
}

func readOpenCodeFile(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf openCodeSessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return nil, err
	}
	if sf.ID == "" {
		return nil, fmt.Errorf("opencode session missing id: %s", path)
	}

	root := openCodeStorageRoot(path)
	messageDir := filepath.Join(root, "message", sf.ID)
	files, err := os.ReadDir(messageDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	var records []openCodeMessageFile
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(messageDir, f.Name()))
		if err != nil {
			continue
		}
		var msg openCodeMessageFile
		if err := json.Unmarshal(data, &msg); err != nil || msg.ID == "" {
			continue
		}
		records = append(records, msg)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Time.Created < records[j].Time.Created
	})

	var entries []Entry
	metaTime := unixMillis(sf.Time.Created)
	if metaTime.IsZero() && len(records) > 0 {
		metaTime = unixMillis(records[0].Time.Created)
	}
	entries = append(entries, Entry{
		Type:      "session_meta",
		Subtype:   "session_meta",
		SessionID: sf.ID,
		Timestamp: metaTime,
		CWD:       sf.Directory,
		Version:   sf.Version,
		Slug:      sf.Slug,
		Source:    "opencode",
	})
	if sf.Title != "" {
		entries = append(entries, Entry{
			Type:        "custom-title",
			SessionID:   sf.ID,
			Timestamp:   metaTime,
			CWD:         sf.Directory,
			Version:     sf.Version,
			Slug:        sf.Slug,
			CustomTitle: sf.Title,
			Source:      "opencode",
		})
	}

	for _, rec := range records {
		parts := readOpenCodeParts(ctx, root, rec.ID)
		blocks := openCodeBlocks(parts)
		if len(blocks) == 0 && rec.Summary.Title != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: rec.Summary.Title})
		}
		if len(blocks) == 0 {
			continue
		}
		raw, err := json.Marshal(blocks)
		if err != nil {
			return nil, err
		}
		t := unixMillis(rec.Time.Created)
		usage := &Usage{
			InputTokens:              rec.Tokens.Input,
			OutputTokens:             rec.Tokens.Output,
			CacheReadInputTokens:     rec.Tokens.Cache.Read,
			CacheCreationInputTokens: rec.Tokens.Cache.Write,
		}
		model := rec.ModelID
		if model == "" {
			model = rec.Model.ModelID
		}
		cwd := sf.Directory
		if rec.Path.CWD != "" {
			cwd = rec.Path.CWD
		}
		entries = append(entries, Entry{
			Type:       rec.Role,
			SessionID:  sf.ID,
			UUID:       rec.ID,
			ParentUUID: rec.ParentID,
			Timestamp:  t,
			CWD:        cwd,
			Version:    sf.Version,
			Slug:       sf.Slug,
			Source:     "opencode",
			Usage:      usage,
			Message: &Message{
				ID:      rec.ID,
				Role:    rec.Role,
				Content: raw,
				Model:   model,
				Usage:   usage,
			},
		})
	}
	return entries, nil
}

func readOpenCodeParts(ctx context.Context, root, messageID string) []openCodePartFile {
	partDir := filepath.Join(root, "part", messageID)
	files, err := os.ReadDir(partDir)
	if err != nil {
		return nil
	}
	var parts []openCodePartFile
	for _, f := range files {
		if ctx.Err() != nil {
			return parts
		}
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(partDir, f.Name()))
		if err != nil {
			continue
		}
		var part openCodePartFile
		if err := json.Unmarshal(data, &part); err != nil || part.ID == "" {
			continue
		}
		parts = append(parts, part)
	}
	sort.Slice(parts, func(i, j int) bool {
		ti, tj := openCodePartMillis(parts[i]), openCodePartMillis(parts[j])
		if ti == tj {
			return parts[i].ID < parts[j].ID
		}
		return ti < tj
	})
	return parts
}

func openCodeBlocks(parts []openCodePartFile) []ContentBlock {
	var blocks []ContentBlock
	for _, part := range parts {
		switch part.Type {
		case "text":
			if part.Text != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: part.Text})
			}
		case "tool":
			input, _ := json.Marshal(part.State["input"])
			toolUseID := part.CallID
			if toolUseID == "" {
				toolUseID = part.ID
			}
			blocks = append(blocks, ContentBlock{
				Type:  "tool_use",
				ID:    toolUseID,
				Name:  openCodeToolName(part.Tool),
				Input: input,
			})
			if result, ok := openCodeToolResult(part, toolUseID); ok {
				blocks = append(blocks, result)
			}
		}
	}
	return blocks
}

func openCodeToolResult(part openCodePartFile, toolUseID string) (ContentBlock, bool) {
	content, hasOutput := openCodeStateString(part.State, "output")
	errText, hasError := openCodeStateString(part.State, "error")
	if !hasOutput && !hasError {
		return ContentBlock{}, false
	}
	if !hasOutput {
		content = errText
	}
	return ContentBlock{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   hasError,
	}, true
}

func openCodeStateString(state map[string]any, key string) (string, bool) {
	v, ok := state[key]
	if !ok || v == nil {
		return "", false
	}
	if s, ok := v.(string); ok {
		return s, true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func openCodeToolName(name string) string {
	switch strings.ToLower(name) {
	case "bash":
		return "Bash"
	case "edit":
		return "Edit"
	case "write":
		return "Write"
	case "read":
		return "Read"
	case "grep":
		return "Grep"
	case "glob":
		return "Glob"
	case "task":
		return "Task"
	default:
		return name
	}
}

func openCodePartMillis(part openCodePartFile) int64 {
	if part.Time.Created != 0 {
		return part.Time.Created
	}
	if stateTime, ok := part.State["time"].(map[string]any); ok {
		if start, ok := stateTime["start"].(float64); ok {
			return int64(start)
		}
	}
	return 0
}

func openCodeStorageRoot(path string) string {
	dir := filepath.Dir(path)
	for filepath.Base(dir) != "session" {
		next := filepath.Dir(dir)
		if next == dir {
			return filepath.Dir(filepath.Dir(path))
		}
		dir = next
	}
	return filepath.Dir(dir)
}

func unixMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond)).UTC()
}
