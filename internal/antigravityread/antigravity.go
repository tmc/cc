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
	"regexp"
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
		if _, transcript := TranscriptFile(path); transcript != nil {
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

// TranscriptFile returns the preferred transcript in dir and its file info.
// If neither transcript exists, it returns an empty path and nil.
func TranscriptFile(dir string) (string, os.FileInfo) {
	for _, name := range []string{"transcript.jsonl", "transcript_full.jsonl"} {
		path := filepath.Join(dir, ".system_generated", "logs", name)
		info, err := os.Stat(path)
		if err == nil && info.Mode().IsRegular() {
			return path, info
		}
	}
	return "", nil
}

type agStep struct {
	StepIndex       int          `json:"step_index"`
	Source          string       `json:"source"`
	Type            string       `json:"type"`
	Status          string       `json:"status"`
	CreatedAt       string       `json:"created_at"`
	Model           string       `json:"model,omitempty"`
	InputTokens     int          `json:"input_tokens,omitempty"`
	CacheReadTokens int          `json:"cache_read_tokens,omitempty"`
	OutputTokens    int          `json:"output_tokens,omitempty"`
	Content         string       `json:"content,omitempty"`
	Thinking        string       `json:"thinking,omitempty"`
	ToolCalls       []agToolCall `json:"tool_calls,omitempty"`
	TruncatedFields []string     `json:"truncated_fields,omitempty"`
	Error           string       `json:"error,omitempty"`
	ErrorCode       int          `json:"error_code,omitempty"`
}

type agToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type pendingToolCall struct {
	id        string
	name      string
	questions []string // ask_question question texts, for pairing answers
}

// askQuestions extracts the question texts from ask_question args, whose
// "questions" field arrives double-encoded (a JSON string holding a JSON
// array).
func askQuestions(args json.RawMessage) []string {
	var w struct {
		Questions json.RawMessage `json:"questions"`
	}
	if json.Unmarshal(args, &w) != nil || len(w.Questions) == 0 {
		return nil
	}
	raw := w.Questions
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var qs []struct {
		Question string `json:"question"`
	}
	if json.Unmarshal(raw, &qs) != nil {
		return nil
	}
	var out []string
	for _, q := range qs {
		out = append(out, q.Question)
	}
	return out
}

// askAnswerRe matches the "A1: label" lines an ASK_QUESTION step records.
var askAnswerRe = regexp.MustCompile(`(?m)^A(\d+): (.*)$`)

// parseAskAnswers pairs an ASK_QUESTION step's "A<n>: label" lines with the
// asked questions, producing the same answered shape Claude Code persists.
func parseAskAnswers(questions []string, content string) *ccmodel.ToolUseResult {
	ms := askAnswerRe.FindAllStringSubmatch(content, -1)
	if len(ms) == 0 {
		return nil
	}
	res := &ccmodel.ToolUseResult{Type: "tool_result", Answers: make(map[string]string)}
	for _, q := range questions {
		res.Questions = append(res.Questions, ccmodel.AskedQuestion{Question: q})
	}
	for _, m := range ms {
		i, _ := strconv.Atoi(m[1])
		key := fmt.Sprintf("A%s", m[1])
		if i >= 1 && i <= len(questions) {
			key = questions[i-1]
		}
		res.Answers[key] = m[2]
	}
	return res
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
		if transcript, _ := TranscriptFile(path); transcript != "" {
			return readTranscriptFile(ctx, transcript, filepath.Base(path), path)
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

	var steps []agStep
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
		steps = append(steps, step)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var fullSteps map[string][]agStep
	fullPath := filepath.Join(filepath.Dir(path), "transcript_full.jsonl")
	if path != fullPath {
		if fInfo, err := os.Stat(fullPath); err == nil && !fInfo.IsDir() {
			fullSteps, err = readFullSteps(ctx, fullPath)
			if err != nil {
				return nil, err
			}
		}
	}
	restoreTruncatedFields(steps, fullSteps)

	cwd := antigravityWorkspace(sessionDir)
	if cwd == "" {
		cwd = extractFallbackCWD(steps)
	}

	var entries []ccmodel.Entry
	var pendingToolCalls []pendingToolCall
	sessionID = antigravitySessionID(sessionID)

	for stepNumber, step := range steps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		parsedTime := parseTime(step.CreatedAt)

		// On the first entry, prepend a session_meta entry.
		if len(entries) == 0 {
			entries = append(entries, ccmodel.Entry{
				Type:      "session_meta",
				Subtype:   "session_meta",
				SessionID: sessionID,
				Timestamp: parsedTime,
				CWD:       cwd,
				Source:    "antigravity",
			})
		}

		if len(pendingToolCalls) > 0 && isToolResultStep(step.Type, pendingToolCalls[0].name) {
			tc := pendingToolCalls[0]
			pendingToolCalls = pendingToolCalls[1:]
			e := antigravityToolResult(sessionID, cwd, parsedTime, tc.id, step.Content)
			if normalizeToolName(tc.name) == "AskUserQuestion" {
				if res := parseAskAnswers(tc.questions, step.Content); res != nil {
					e.ToolUseResult = res
				}
			}
			entries = append(entries, e)
			continue
		}

		switch strings.ToUpper(step.Type) {
		case "USER_INPUT":
			msgContent, _ := json.Marshal([]ccmodel.ContentBlock{
				{
					Type: "text",
					Text: step.Content,
				},
			})
			entries = append(entries, ccmodel.Entry{
				Type:      "user",
				SessionID: sessionID,
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
					Type: "thinking",
					Text: step.Thinking,
				})
			}
			if step.Content != "" {
				blocks = append(blocks, ccmodel.ContentBlock{
					Type: "text",
					Text: step.Content,
				})
			}
			if len(step.ToolCalls) > 0 {
				pendingToolCalls = pendingToolCalls[:0]
			}
			for i, tc := range step.ToolCalls {
				toolUseID := fmt.Sprintf("ag-tc-%d-%d", stepNumber, i)
				pendingToolCalls = append(pendingToolCalls, pendingToolCall{
					id:        toolUseID,
					name:      tc.Name,
					questions: askQuestions(tc.Args),
				})
				blocks = append(blocks, ccmodel.ContentBlock{
					Type:  "tool_use",
					ID:    toolUseID,
					Name:  normalizeToolName(tc.Name),
					Input: cleanArgs(tc.Args),
				})
			}
			msgContent, _ := json.Marshal(blocks)
			var usage *ccmodel.Usage
			if step.InputTokens > 0 || step.CacheReadTokens > 0 || step.OutputTokens > 0 {
				usage = &ccmodel.Usage{
					InputTokens:          step.InputTokens,
					CacheReadInputTokens: step.CacheReadTokens,
					OutputTokens:         step.OutputTokens,
				}
			}
			entries = append(entries, ccmodel.Entry{
				Type:      "assistant",
				SessionID: sessionID,
				Timestamp: parsedTime,
				Source:    "antigravity",
				CWD:       cwd,
				Message: &ccmodel.Message{
					Role:    "assistant",
					Content: msgContent,
					Model:   step.Model,
					Usage:   usage,
				},
			})

		case "COMPACTION", "COMPACTION_BOUNDARY":
			if step.Content != "" {
				entries = append(entries, ccmodel.Entry{
					Type:      "system",
					Subtype:   "compact_boundary",
					SessionID: sessionID,
					Timestamp: parsedTime,
					Source:    "antigravity",
					IsMeta:    true,
					Content:   step.Content,
					CWD:       cwd,
				})
			}

		case "ERROR_MESSAGE":
			// API errors surface as visible entries, matching how the other
			// readers normalize them (see ccmodel.Entry.IsAPIErrorMessage).
			txt := step.Content
			if txt == "" && step.Error != "" {
				txt = "Error: " + step.Error
			}
			if txt != "" {
				content, _ := json.Marshal([]ccmodel.ContentBlock{{Type: "text", Text: txt}})
				entries = append(entries, ccmodel.Entry{
					Type:              "assistant",
					Subtype:           "error_message",
					SessionID:         sessionID,
					Timestamp:         parsedTime,
					Source:            "antigravity",
					IsAPIErrorMessage: true,
					CWD:               cwd,
					Message: &ccmodel.Message{
						Role:    "assistant",
						Content: content,
					},
				})
			}

		default:
			// CONVERSATION_HISTORY, SYSTEM_MESSAGE, etc.
			if step.Content != "" {
				entries = append(entries, ccmodel.Entry{
					Type:      "system",
					Subtype:   strings.ToLower(step.Type),
					SessionID: sessionID,
					Timestamp: parsedTime,
					Source:    "antigravity",
					IsMeta:    true,
					Content:   step.Content,
					CWD:       cwd,
				})
			}
		}
	}

	return entries, nil
}

func readFullSteps(ctx context.Context, path string) (map[string][]agStep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, ccmodel.InitialBufferSize), ccmodel.MaxLineSize)

	fullSteps := make(map[string][]agStep)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var step agStep
		if err := json.Unmarshal(line, &step); err == nil {
			key := antigravityStepKey(step)
			fullSteps[key] = append(fullSteps[key], step)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return fullSteps, nil
}

func antigravityStepKey(step agStep) string {
	return fmt.Sprintf("%d:%s:%s", step.StepIndex, step.Type, step.CreatedAt)
}

func restoreTruncatedFields(steps []agStep, fullSteps map[string][]agStep) {
	for i := range steps {
		matches := fullSteps[antigravityStepKey(steps[i])]
		if len(matches) == 0 {
			continue
		}
		fullSteps[antigravityStepKey(steps[i])] = matches[1:]
		if len(steps[i].TruncatedFields) == 0 {
			continue
		}
		full := matches[0]
		for _, field := range steps[i].TruncatedFields {
			switch strings.ToLower(field) {
			case "content":
				steps[i].Content = full.Content
			case "thinking":
				steps[i].Thinking = full.Thinking
			case "tool_calls":
				steps[i].ToolCalls = full.ToolCalls
			}
		}
	}
}

func antigravityToolResult(sessionID, cwd string, timestamp time.Time, toolUseID, output string) ccmodel.Entry {
	content, exitCode, success := cleanAntigravityOutput(output)
	msgContent, _ := json.Marshal([]ccmodel.ContentBlock{{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
		IsError:   !success,
	}})
	res := &ccmodel.ToolUseResult{Type: "tool_result", Stdout: content, Success: success}
	if !success {
		res.Error = content
	}
	if exitCode != nil {
		res.Task = &ccmodel.TaskResult{ExitCode: exitCode}
	}
	return ccmodel.Entry{
		Type:          "user",
		SessionID:     sessionID,
		Timestamp:     timestamp,
		Source:        "antigravity",
		CWD:           cwd,
		Message:       &ccmodel.Message{Role: "user", Content: msgContent},
		ToolUseResult: res,
	}
}

func isToolResultStep(stepType, toolName string) bool {
	if strings.EqualFold(stepType, toolName) || normalizeToolName(stepType) == normalizeToolName(toolName) {
		return true
	}
	switch strings.ToUpper(stepType) {
	case "SYSTEM_MESSAGE", "CHECKPOINT", "USER_INPUT", "PLANNER_RESPONSE", "CONVERSATION_HISTORY", "COMPACTION", "COMPACTION_BOUNDARY":
		return false
	default:
		return true
	}
}

func antigravitySessionID(id string) string {
	if strings.HasPrefix(id, "ag-") {
		return id
	}
	return "ag-" + id
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
	case "view_file", "read_file":
		return "View"
	case "replace_file_content", "multi_replace_file_content", "write_to_file", "write_file":
		return "Write"
	case "edit_file":
		return "Edit"
	case "list_dir", "list_directory":
		return "LS"
	case "grep_search":
		return "Grep"
	case "search_web":
		return "WebSearch"
	case "read_url_content":
		return "WebFetch"
	case "invoke_subagent":
		return "Task"
	case "ask_question":
		return "AskUserQuestion"
	case "ask_permission":
		return "Interaction"
	case "call_mcp_tool":
		return "MCP"
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

	id := antigravitySessionID(filepath.Base(dir))
	cwd := antigravityWorkspace(dir)
	if cwd == "" {
		for _, file := range files {
			if data, err := os.ReadFile(file.path); err == nil {
				if foundCwd := extractCWDFromText(string(data)); foundCwd != "" {
					cwd = foundCwd
					break
				}
			}
		}
	}

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
			SessionID: id,
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
			SessionID: id,
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
	path = strings.TrimRight(strings.TrimSpace(path), ".,;:")
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
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
		if base != "" {
			path = filepath.Join(base, path)
		} else {
			return ""
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

var (
	userInfoBlockRE      = regexp.MustCompile(`(?is)<user_information>(.*?)</user_information>`)
	userInfoMappingRE    = regexp.MustCompile(`(?m)^\s*([^\n\r]+?)\s+->`)
	userInfoWorkingDirRE = regexp.MustCompile(`(?i)(?:Working directory|CWD|Workspace):\s*([^\n\r]+)`)
	toolArgCwdRE         = regexp.MustCompile(`"(?:Cwd|cwd)"\s*:\s*"([^"]+)"`)
)

func extractFallbackCWD(steps []agStep) string {
	for _, step := range steps {
		if strings.EqualFold(step.Type, "USER_INPUT") && step.Content != "" {
			if cwd := extractCWDFromText(step.Content); cwd != "" {
				return cwd
			}
		}
	}
	for _, step := range steps {
		for _, tc := range step.ToolCalls {
			if len(tc.Args) == 0 {
				continue
			}
			if cwd := extractCWDFromArgs(tc.Args); cwd != "" {
				return cwd
			}
		}
	}
	return ""
}

func extractCWDFromText(text string) string {
	for _, match := range userInfoBlockRE.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 {
			block := match[1]
			if m := userInfoMappingRE.FindStringSubmatch(block); len(m) > 1 {
				p := strings.TrimRight(strings.TrimSpace(m[1]), ".,;:")
				if path := cleanAntigravityWorkspacePath("", p); path != "" && filepath.IsAbs(path) {
					return path
				}
			}
			if m := userInfoWorkingDirRE.FindStringSubmatch(block); len(m) > 1 {
				p := strings.TrimRight(strings.TrimSpace(m[1]), ".,;:")
				if path := cleanAntigravityWorkspacePath("", p); path != "" && filepath.IsAbs(path) {
					return path
				}
			}
		}
	}
	if m := userInfoMappingRE.FindStringSubmatch(text); len(m) > 1 {
		p := strings.TrimRight(strings.TrimSpace(m[1]), ".,;:")
		if path := cleanAntigravityWorkspacePath("", p); path != "" && filepath.IsAbs(path) {
			return path
		}
	}
	if m := userInfoWorkingDirRE.FindStringSubmatch(text); len(m) > 1 {
		p := strings.TrimRight(strings.TrimSpace(m[1]), ".,;:")
		if path := cleanAntigravityWorkspacePath("", p); path != "" && filepath.IsAbs(path) {
			return path
		}
	}
	return ""
}

func extractCWDFromArgs(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		for k, v := range m {
			if strings.EqualFold(k, "cwd") {
				if s, ok := v.(string); ok && s != "" {
					s = cleanAntigravityString(s)
					if path := cleanAntigravityWorkspacePath("", s); path != "" && filepath.IsAbs(path) {
						return path
					}
				}
			}
		}
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		str = cleanAntigravityString(str)
		var m2 map[string]any
		if err := json.Unmarshal([]byte(str), &m2); err == nil {
			for k, v := range m2 {
				if strings.EqualFold(k, "cwd") {
					if s, ok := v.(string); ok && s != "" {
						s = cleanAntigravityString(s)
						if path := cleanAntigravityWorkspacePath("", s); path != "" && filepath.IsAbs(path) {
							return path
						}
					}
				}
			}
		}
		if m := toolArgCwdRE.FindStringSubmatch(str); len(m) > 1 {
			if path := cleanAntigravityWorkspacePath("", m[1]); path != "" && filepath.IsAbs(path) {
				return path
			}
		}
	}
	if m := toolArgCwdRE.FindStringSubmatch(string(raw)); len(m) > 1 {
		if path := cleanAntigravityWorkspacePath("", m[1]); path != "" && filepath.IsAbs(path) {
			return path
		}
	}
	return ""
}

func cleanAntigravityString(s string) string {
	s = strings.TrimSpace(s)
	for range 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(s), &unquoted); err != nil {
			break
		}
		s = strings.TrimSpace(unquoted)
	}
	return s
}
