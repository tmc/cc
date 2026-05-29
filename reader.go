package cc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/ccpaths"
)

// Scanner buffer sizes for JSONL session lines. Sessions can carry
// large tool_result payloads (file contents, base64-encoded images),
// so MaxLineSize is generous.
const (
	initialBufferSize = 256 * 1024
	MaxLineSize       = 10 * 1024 * 1024
)

// Reader reads entries from a JSONL session file.
// The zero value is not usable; use NewReader.
type Reader struct {
	ctx     context.Context
	scanner *bufio.Scanner
	err     error
	entry   Entry
	n       int
}

// NewReader creates a Reader from an io.Reader. The context is checked
// cooperatively during Next so callers can cancel long reads.
func NewReader(ctx context.Context, r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, initialBufferSize), MaxLineSize)
	return &Reader{ctx: ctx, scanner: s}
}

// Next advances to the next entry. Returns false at EOF, on error, or when
// the reader's context is canceled.
func (r *Reader) Next() bool {
	for r.scanner.Scan() {
		r.n++
		if r.n%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				r.err = err
				return false
			}
		}
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
	case "turn_context":
		return decodeCodexTurnContext(env)
	case "response_item":
		return decodeCodexResponseItem(env)
	case "web_search_call":
		return decodeCodexWebSearch(env)
	case "compacted":
		return Entry{
			Type:      "system",
			Subtype:   "compact_boundary",
			Timestamp: env.Timestamp,
		}, true
	case "event_msg", "reasoning", "ghost_snapshot":
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
		return Entry{}, false
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

	return Entry{
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

func decodeCodexTurnContext(env codexEnvelope) (Entry, bool) {
	var payload struct {
		TurnID string `json:"turn_id"`
		CWD    string `json:"cwd"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		return Entry{}, false
	}
	if payload.CWD == "" {
		return Entry{}, false
	}
	return Entry{
		Type:      "system",
		Subtype:   "turn_context",
		UUID:      payload.TurnID,
		Timestamp: env.Timestamp,
		CWD:       payload.CWD,
		IsMeta:    true,
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
		if payload.Role != "user" && payload.Role != "assistant" && payload.Role != "developer" {
			return Entry{}, false
		}
		blocks := codexTextBlocks(payload.Content)
		if len(blocks) == 0 {
			return Entry{}, false
		}
		content, _ := json.Marshal(blocks)
		entryType := payload.Role
		if payload.Role == "developer" {
			entryType = "system"
		}
		entry := Entry{
			Type:      entryType,
			Timestamp: env.Timestamp,
			Phase:     payload.Phase,
			Message: &Message{
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

	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		output := payload.Output
		if payload.Type == "tool_search_output" && len(output) == 0 {
			output = env.Payload
		}
		stdout, status, success, errText := parseCodexToolOutput(output)
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
		return Entry{}, false
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
		return Entry{}, false
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
	if blocks := decodeCodexContentBlocks(raw); len(blocks) > 0 {
		return blocks
	}
	text := strings.TrimSpace(ExtractAnyText(raw))
	if text == "" {
		return nil
	}
	return []ContentBlock{{Type: "text", Text: text}}
}

func decodeCodexContentBlocks(raw json.RawMessage) []ContentBlock {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	blocks := make([]ContentBlock, 0, len(items))
	for _, item := range items {
		typ, _ := item["type"].(string)
		switch typ {
		case "input_text", "output_text", "text":
			text, _ := item["text"].(string)
			text = strings.TrimSpace(text)
			if text != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: text})
			}
		case "input_image", "image", "local_image":
			block := ContentBlock{Type: typ}
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

// Entry returns the current entry.
func (r *Reader) Entry() Entry { return r.entry }

// Err returns any error from scanning.
func (r *Reader) Err() error { return r.err }

// ReadAll reads all entries from the reader.
func ReadAll(ctx context.Context, r io.Reader) ([]Entry, error) {
	rd := NewReader(ctx, r)
	var entries []Entry
	for rd.Next() {
		entries = append(entries, rd.Entry())
	}
	return entries, rd.Err()
}

// ReadFile reads all entries from a JSONL file.
func ReadFile(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAll(ctx, f)
}

// ErrTailInvalid reports that an offset passed to [ReadFileFrom] cannot be
// safely tailed — the file is shorter than the offset (truncation or rewrite),
// or the offset does not sit just past a newline. Callers should fall back to a
// full [ReadFileWithOffset].
var ErrTailInvalid = errors.New("tail offset invalid; reparse from start")

// ReadFileFrom reads the entries appended to a JSONL file after the given byte
// offset, which must point just past a line terminator (or be 0). It returns
// the new complete entries and the byte offset just past the last complete line
// read. A trailing line without a final newline is treated as still being
// written: it is decoded and returned separately as partial (nil if absent or
// undecodable), but is not included in newOffset, so it is re-read once
// complete. Callers that cache by offset must store only entries and return
// entries+partial — folding partial into the cache would double-count it when
// the line later completes.
//
// Because each JSONL line decodes independently (see decodeEntryLine), the
// entries (plus partial) returned are exactly those a full [ReadFile] would
// yield for the same byte range. ReadFileFrom returns [ErrTailInvalid] when
// offset is past the file size or not on a line boundary; the caller should
// reparse the whole file in that case.
func ReadFileFrom(ctx context.Context, path string, offset int64) (entries []Entry, newOffset int64, partial *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, 0, nil, err
	}
	size := fi.Size()
	if offset > size {
		return nil, 0, nil, ErrTailInvalid // file shrank: truncation or rewrite.
	}
	if offset == size {
		return nil, offset, nil, nil // nothing appended.
	}
	if offset > 0 {
		// Confirm the offset sits just past a newline, so we start on a clean
		// line boundary rather than mid-line.
		var b [1]byte
		if _, err := f.ReadAt(b[:], offset-1); err != nil {
			return nil, 0, nil, err
		}
		if b[0] != '\n' {
			return nil, 0, nil, ErrTailInvalid
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, nil, err
	}
	entries, consumed, partial, err := readComplete(ctx, f)
	if err != nil {
		return nil, 0, nil, err
	}
	return entries, offset + consumed, partial, nil
}

// ReadFileWithOffset reads all entries from a JSONL file and also returns the
// byte offset just past the last complete (newline-terminated) line, suitable
// for a later [ReadFileFrom]. A trailing line without a final newline is
// decoded and returned as partial (nil if absent or undecodable) but is not
// reflected in the offset, so it is re-read once completed. See [ReadFileFrom]
// for the caching contract on partial.
func ReadFileWithOffset(ctx context.Context, path string) (entries []Entry, offset int64, partial *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()
	return readComplete(ctx, f)
}

// readComplete scans r, decoding each complete newline-terminated line, and
// returns the decoded entries plus the number of bytes consumed up to and
// including the last newline. Bytes after the final newline (an unterminated
// trailing line) are not counted in consumed — so the offset always lands on a
// line boundary — but are decoded and returned as partial (nil if absent or
// undecodable), matching what [ReadFile]'s scanner yields for the final token.
// A single line longer than [MaxLineSize] returns the entries decoded so far
// plus [bufio.ErrTooLong], exactly as [ReadFile] does, so all readers agree on
// which files are unparseable.
func readComplete(ctx context.Context, r io.Reader) (entries []Entry, consumed int64, partial *Entry, err error) {
	br := bufio.NewReaderSize(r, initialBufferSize)
	n := 0
	for {
		line, complete, readErr := readLine(br)
		if readErr != nil && readErr != io.EOF {
			// ErrTooLong (line content reached MaxLineSize) or an I/O error;
			// either way report it just as ReadFile's scanner would, with the
			// entries decoded so far.
			return entries, consumed, nil, readErr
		}
		if complete {
			consumed += int64(len(line)) + 1 // include the trailing newline.
			n++
			if n%256 == 0 {
				if cerr := ctx.Err(); cerr != nil {
					return entries, consumed, nil, cerr
				}
			}
			if entry, ok := decodeLine(line); ok {
				entries = append(entries, entry)
			}
		} else if len(line) > 0 {
			// An unterminated final line: decode it for the returned set
			// (matching ReadFile) but leave it out of consumed so it is re-read
			// once a newline lands.
			if entry, ok := decodeLine(line); ok {
				partial = &entry
			}
		}
		if readErr == io.EOF {
			return entries, consumed, partial, nil
		}
	}
}

// readLine returns the next line from br without its trailing newline. complete
// reports whether the line was newline-terminated (false for an unterminated
// final line at EOF). It returns [bufio.ErrTooLong] when a single line's
// content reaches [MaxLineSize] — matching the bufio.Scanner cap [ReadFile]
// uses — so memory stays bounded even on a pathological multi-gigabyte line.
func readLine(br *bufio.Reader) (line []byte, complete bool, err error) {
	for {
		frag, e := br.ReadSlice('\n')
		if e == nil {
			if line == nil {
				return frag[:len(frag)-1], true, nil // common case: no fragment buffering.
			}
			line = append(line, frag[:len(frag)-1]...)
			if len(line) >= MaxLineSize {
				return nil, false, bufio.ErrTooLong
			}
			return line, true, nil
		}
		if e == bufio.ErrBufferFull {
			line = append(line, frag...)
			if len(line) >= MaxLineSize {
				return nil, false, bufio.ErrTooLong
			}
			continue
		}
		// e is io.EOF or an I/O error: frag holds the trailing unterminated bytes.
		line = append(line, frag...)
		if len(line) >= MaxLineSize {
			return nil, false, bufio.ErrTooLong
		}
		return line, false, e
	}
}

// decodeLine trims a trailing CR and skips an empty line, then decodes it the
// same way [Reader.Next] does, so terminated and unterminated lines decode
// identically.
func decodeLine(content []byte) (Entry, bool) {
	if len(content) > 0 && content[len(content)-1] == '\r' {
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		return Entry{}, false
	}
	return decodeEntryLine(content)
}

// ReadFileWithSubagents reads a session JSONL file and merges entries from any
// subagent files found at <path-without-.jsonl>/subagents/agent-*.jsonl.
// Subagent entries are tagged with AgentID (from the filename) and IsSidechain=true.
// The merged result is sorted by timestamp.
func ReadFileWithSubagents(ctx context.Context, path string) ([]Entry, error) {
	entries, err := ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	subs, err := ReadSubagents(ctx, path)
	if err != nil {
		return entries, nil
	}
	entries = append(entries, subs...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

// ReadSubagents reads entries from subagent files under
// <path-without-.jsonl>/subagents/agent-*.jsonl. Each entry is tagged with
// AgentID derived from the filename and IsSidechain=true. Returns a nil slice
// and nil error if the subagents directory does not exist.
func ReadSubagents(ctx context.Context, path string) ([]Entry, error) {
	subagentDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	infos, err := os.ReadDir(subagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	for _, fi := range infos {
		name := fi.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-acompact") {
			continue
		}
		sub, err := ReadFile(ctx, filepath.Join(subagentDir, name))
		if err != nil {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		for i := range sub {
			if sub[i].AgentID == "" {
				sub[i].AgentID = agentID
			}
			sub[i].IsSidechain = true
		}
		entries = append(entries, sub...)
	}
	return entries, nil
}

// SessionSummary holds summarized metadata for a session file.
// GitBranch is the branch recorded in the session entries; the embedded
// GitContext fields are resolved from the latest CWD on the local
// filesystem and may differ if the worktree has since moved or changed.
type SessionSummary struct {
	SessionID    string   `json:"session_id"`
	File         string   `json:"file"`
	Project      string   `json:"project"`
	CWD          string   `json:"cwd,omitempty"`
	DistinctCWDs []string `json:"distinct_cwds,omitempty"`
	GitBranch    string   `json:"git_branch,omitempty"`
	ccgit.GitContext
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
	seenCWDs := map[string]bool{}
	for _, e := range entries {
		s.TotalLines++
		if e.SessionID != "" && s.SessionID == "" {
			s.SessionID = e.SessionID
		}
		if e.Version != "" && s.Version == "" {
			s.Version = e.Version
		}
		if e.CWD != "" {
			s.CWD = e.CWD
			if !seenCWDs[e.CWD] {
				seenCWDs[e.CWD] = true
				s.DistinctCWDs = append(s.DistinctCWDs, e.CWD)
			}
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
				if e.Message.IsToolResultOnly() {
					break
				}
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
	if s.CWD != "" {
		if ctx, err := ccgit.ResolveGitContext(s.CWD); err == nil {
			s.GitContext = ctx
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
// ~/.gemini/projects/, and ~/.codex/sessions/. It excludes subagent files,
// filters by modification time, and stops early when ctx is canceled.
func FindSessionFiles(ctx context.Context, since time.Duration, project string) ([]string, error) {
	ch, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, err
	}
	gh, _ := ccpaths.GeminiHome()
	xh, _ := ccpaths.CodexHome()

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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := filepath.Walk(dir.path, func(path string, info os.FileInfo, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
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
					if !codexPathMatchesProject(ctx, path, q) {
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
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

func codexPathMatchesProject(ctx context.Context, path, query string) bool {
	entries, err := ReadFile(ctx, path)
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
