// Package antigravityread decodes Google Antigravity (agy) session directories
// and transcript logs into the normalized cc data model.
//
// Antigravity stores sessions as directories containing task artifacts and
// structured JSONL step logs. This package normalizes those logs or fallback
// task files into canonical ccmodel.Entry and ccmodel.Message shapes.
package antigravityread

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc/internal/ccmodel"
)

// IsSessionPath reports whether path is an Antigravity session directory or a
// transcript JSONL file.
func IsSessionPath(path string) bool {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		if _, err := os.Stat(filepath.Join(path, ".system_generated", "logs", "transcript.jsonl")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(path, "task.md")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(path, "agent_prompt.md")); err == nil {
			return true
		}
		return false
	}
	base := filepath.Base(path)
	if base == "transcript.jsonl" || base == "transcript_full.jsonl" {
		dir := filepath.Dir(path)
		if filepath.Base(dir) == "logs" {
			parentDir := filepath.Dir(dir)
			if filepath.Base(parentDir) == ".system_generated" {
				return true
			}
		}
	}
	return false
}

type agStep struct {
	StepIndex int             `json:"step_index"`
	Source    string          `json:"source"`
	Type      string          `json:"type"`
	Status    string          `json:"status"`
	CreatedAt string          `json:"created_at"`
	Content   string          `json:"content,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ToolCalls []agToolCall    `json:"tool_calls,omitempty"`
}

type agToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type pendingToolCall struct {
	id   string
	name string
}

// ReadFile reads and parses an Antigravity session from a directory or a
// transcript file.
func ReadFile(ctx context.Context, path string) ([]ccmodel.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		transcriptPath := filepath.Join(path, ".system_generated", "logs", "transcript.jsonl")
		if _, err := os.Stat(transcriptPath); err == nil {
			return readTranscriptFile(ctx, transcriptPath, filepath.Base(path), path)
		}
		transcriptFull := filepath.Join(path, ".system_generated", "logs", "transcript_full.jsonl")
		if _, err := os.Stat(transcriptFull); err == nil {
			return readTranscriptFile(ctx, transcriptFull, filepath.Base(path), path)
		}
		return readLegacySession(ctx, path)
	}

	sessionID := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	sessionDir := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	return readTranscriptFile(ctx, path, sessionID, sessionDir)
}

func readTranscriptFile(ctx context.Context, path, sessionID, sessionDir string) ([]ccmodel.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, ccmodel.InitialBufferSize), ccmodel.MaxLineSize)

	var entries []ccmodel.Entry
	var pendingToolCalls []pendingToolCall

	// Determine CWD and workspace if available.
	cwd := antigravityWorkspace(sessionDir)

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}

		var step agStep
		if err := json.Unmarshal(line, &step); err != nil {
			continue
		}

		parsedTime := parseTime(step.CreatedAt)

		// On the first entry, prepend a session_meta entry.
		if len(entries) == 0 {
			entries = append(entries, ccmodel.Entry{
				Type:      "session_meta",
				Subtype:   "session_meta",
				SessionID: "ag-" + sessionID,
				Timestamp: parsedTime,
				CWD:       cwd,
				Source:    "antigravity",
			})
		}

		switch step.Type {
		case "USER_INPUT":
			msgContent, _ := json.Marshal([]ccmodel.ContentBlock{
				{
					Type: "text",
					Text: step.Content,
				},
			})
			entries = append(entries, ccmodel.Entry{
				Type:      "user",
				SessionID: "ag-" + sessionID,
				Timestamp: parsedTime,
				Source:    "antigravity",
				CWD:       cwd,
				Message: &ccmodel.Message{
					Role:    "user",
					Content: msgContent,
				},
			})

		case "PLANNER_RESPONSE":
			var blocks []ccmodel.ContentBlock
			if step.Thinking != "" {
				blocks = append(blocks, ccmodel.ContentBlock{
					Type: "text",
					Text: step.Thinking,
				})
			}
			for i, tc := range step.ToolCalls {
				toolUseID := fmt.Sprintf("ag-tc-%d-%d", step.StepIndex, i)
				pendingToolCalls = append(pendingToolCalls, pendingToolCall{
					id:   toolUseID,
					name: tc.Name,
				})
				blocks = append(blocks, ccmodel.ContentBlock{
					Type:  "tool_use",
					ID:    toolUseID,
					Name:  normalizeToolName(tc.Name),
					Input: cleanArgs(tc.Args),
				})
			}
			msgContent, _ := json.Marshal(blocks)
			entries = append(entries, ccmodel.Entry{
				Type:      "assistant",
				SessionID: "ag-" + sessionID,
				Timestamp: parsedTime,
				Source:    "antigravity",
				CWD:       cwd,
				Message: &ccmodel.Message{
					Role:    "assistant",
					Content: msgContent,
				},
			})

		case "RUN_COMMAND", "VIEW_FILE", "LIST_DIRECTORY", "GREP_SEARCH", "SEARCH_WEB", "CODE_ACTION":
			var toolUseID string
			if len(pendingToolCalls) > 0 {
				tc := pendingToolCalls[0]
				pendingToolCalls = pendingToolCalls[1:]
				toolUseID = tc.id
			}

			content, exitCode, success := cleanAntigravityOutput(step.Content)

			msgContent, _ := json.Marshal([]ccmodel.ContentBlock{
				{
					Type:      "tool_result",
					ToolUseID: toolUseID,
					Content:   content,
					IsError:   !success,
				},
			})

			res := &ccmodel.ToolUseResult{
				Type:    "tool_result",
				Stdout:  content,
				Success: success,
			}
			if !success {
				res.Error = content
			}
			if exitCode != nil {
				res.Task = &ccmodel.TaskResult{
					ExitCode: exitCode,
				}
			}

			entries = append(entries, ccmodel.Entry{
				Type:          "user",
				SessionID:     "ag-" + sessionID,
				Timestamp:     parsedTime,
				Source:        "antigravity",
				CWD:           cwd,
				Message: &ccmodel.Message{
					Role:    "user",
					Content: msgContent,
				},
				ToolUseResult: res,
			})

		default:
			// CONVERSATION_HISTORY, SYSTEM_MESSAGE, ERROR_MESSAGE, etc.
			if step.Content != "" {
				entries = append(entries, ccmodel.Entry{
					Type:      "system",
					Subtype:   strings.ToLower(step.Type),
					SessionID: "ag-" + sessionID,
					Timestamp: parsedTime,
					Source:    "antigravity",
					IsMeta:    true,
					Content:   step.Content,
					CWD:       cwd,
				})
			}
		}
	}

	return entries, sc.Err()
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return time.Time{}
		}
	}
	return t.UTC()
}

func normalizeToolName(name string) string {
	switch strings.ToLower(name) {
	case "run_command":
		return "Bash"
	case "view_file":
		return "View"
	case "replace_file_content", "write_to_file":
		return "Write"
	case "list_dir":
		return "LS"
	case "grep_search":
		return "Grep"
	case "search_web":
		return "WebSearch"
	default:
		return name
	}
}

func cleanArgs(raw json.RawMessage) json.RawMessage {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
			(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) ||
			(strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) {
			return json.RawMessage(s)
		}
		b, _ := json.Marshal(s)
		return b
	}
	return raw
}

func cleanAntigravityOutput(s string) (content string, exitCode *int, success bool) {
	success = true
	idx := strings.Index(s, "Output:\n")
	if idx != -1 {
		content = s[idx+len("Output:\n"):]
	} else if idx = strings.Index(s, "Output:\r\n"); idx != -1 {
		content = s[idx+len("Output:\r\n"):]
	} else {
		idx = strings.Index(s, "The following code has been modified")
		if idx != -1 {
			linesIdx := strings.Index(s[idx:], "\n")
			if linesIdx != -1 {
				content = s[idx+linesIdx+1:]
			} else {
				content = s
			}
		} else {
			content = s
		}
	}

	if strings.Contains(s, "The command failed with exit code:") {
		success = false
		lines := strings.Split(s, "\n")
		for _, line := range lines {
			if strings.Contains(line, "The command failed with exit code:") {
				parts := strings.Split(line, "exit code:")
				if len(parts) == 2 {
					valStr := strings.TrimSpace(parts[1])
					if val, err := strconv.Atoi(valStr); err == nil {
						exitCode = &val
					}
				}
			}
		}
	}
	return content, exitCode, success
}

type antigravityArtifact struct {
	name string
	path string
}

type antigravityArtifactMeta struct {
	ArtifactType string `json:"artifactType"`
	Summary      string `json:"summary"`
	UpdatedAt    string `json:"updatedAt"`
	Version      string `json:"version"`
}

type antigravityWorkspaceFile struct {
	Folders []struct {
		Path string `json:"path"`
	} `json:"folders"`
}

func readLegacySession(ctx context.Context, dir string) ([]ccmodel.Entry, error) {
	files, err := antigravityArtifactFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("empty antigravity brain session: %s", dir)
	}

	id := filepath.Base(dir)
	cwd := antigravityWorkspace(dir)

	var entries []ccmodel.Entry
	var firstTime time.Time

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(file.path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}

		meta := readAntigravityArtifactMeta(file.path + ".metadata.json")
		t := antigravityArtifactTime(meta, file.path)
		if firstTime.IsZero() || (!t.IsZero() && t.Before(firstTime)) {
			firstTime = t
		}

		role := "assistant"
		if file.name == "task.md" || file.name == "agent_prompt.md" {
			role = "user"
		}

		msgContent, _ := json.Marshal([]ccmodel.ContentBlock{
			{
				Type: "text",
				Text: text,
			},
		})

		entries = append(entries, ccmodel.Entry{
			Type:      role,
			SessionID: "ag-" + id,
			UUID:      id + ":" + file.name,
			Timestamp: t,
			CWD:       cwd,
			Source:    "antigravity",
			Message: &ccmodel.Message{
				ID:      id + ":" + file.name,
				Role:    role,
				Content: msgContent,
			},
		})
	}

	if len(entries) > 0 {
		// Prepend session_meta
		metaEntry := ccmodel.Entry{
			Type:      "session_meta",
			Subtype:   "session_meta",
			SessionID: "ag-" + id,
			Timestamp: firstTime,
			CWD:       cwd,
			Source:    "antigravity",
		}
		entries = append([]ccmodel.Entry{metaEntry}, entries...)
	}

	return entries, nil
}

func antigravityArtifactFiles(dir string) ([]antigravityArtifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []antigravityArtifact
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".metadata.json") || strings.Contains(name, ".resolved") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".md" && ext != ".txt" {
			continue
		}
		files = append(files, antigravityArtifact{
			name: name,
			path: filepath.Join(dir, name),
		})
	}
	sort.Slice(files, func(i, j int) bool {
		ri, rj := antigravityArtifactRank(files[i].name), antigravityArtifactRank(files[j].name)
		if ri != rj {
			return ri < rj
		}
		return files[i].name < files[j].name
	})
	return files, nil
}

func antigravityArtifactRank(name string) int {
	switch name {
	case "task.md":
		return 0
	case "agent_prompt.md":
		return 1
	case "implementation_plan.md":
		return 2
	case "walkthrough.md":
		return 3
	}
	if strings.HasSuffix(name, ".md") {
		return 4
	}
	return 5
}

func readAntigravityArtifactMeta(path string) antigravityArtifactMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return antigravityArtifactMeta{}
	}
	var meta antigravityArtifactMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return antigravityArtifactMeta{}
	}
	return meta
}

func antigravityArtifactTime(meta antigravityArtifactMeta, path string) time.Time {
	if meta.UpdatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, meta.UpdatedAt); err == nil {
			return t
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func antigravityWorkspace(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".code-workspace") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ws antigravityWorkspaceFile
		if err := json.Unmarshal(data, &ws); err != nil {
			continue
		}
		for _, folder := range ws.Folders {
			if folder.Path == "" {
				continue
			}
			return cleanAntigravityWorkspacePath(dir, folder.Path)
		}
	}
	return ""
}

func cleanAntigravityWorkspacePath(base, path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			path = home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}
