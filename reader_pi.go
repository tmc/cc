package cc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pi (pi-coding-agent) stores one JSONL file per session under
// <agent-dir>/sessions/<encoded-cwd>/<timestamp>_<uuid>.jsonl. The first line
// is a "session" header; subsequent lines are tree-structured entries keyed by
// id/parentId. Tool results are recorded as their own "message" entries with
// role "toolResult" rather than as content blocks, and reasoning is carried in
// "thinking" blocks. This reader normalizes that layout into the canonical
// Entry/Message/ContentBlock shape with Source "pi".
//
// The format is documented in pi's TypeScript declarations (session-manager,
// pi-ai content blocks); the structs below mirror only the fields cc consumes.

type piSessionHeader struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type piEntry struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	ParentID  string     `json:"parentId"`
	Timestamp string     `json:"timestamp"`
	Message   *piMessage `json:"message"`
}

type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model"`
	Usage      *piUsage        `json:"usage"`
	StopReason string          `json:"stopReason"`
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
}

type piUsage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Data      string          `json:"data"`
	MimeType  string          `json:"mimeType"`
}

// IsPiSessionPath reports whether path looks like a pi (pi-coding-agent)
// session file. It is exported so commands can detect pi files using the same
// rule as [ReadFile].
func IsPiSessionPath(path string) bool { return isPiSessionPath(path) }

// isPiSessionPath reports whether path is a pi session file. The fast path
// matches pi's default ~/.pi/agent/sessions/<project>/<file>.jsonl layout
// without I/O. For other layouts (a PI_CODING_AGENT_DIR override that is not
// named "agent"), it falls back to sniffing the first line for a pi session
// header, which positively distinguishes pi files from Claude/Codex .jsonl
// files that share the .jsonl extension. The sniff only runs for .jsonl files
// sitting under a "sessions" directory, so the common case stays cheap.
func isPiSessionPath(path string) bool {
	if !strings.HasSuffix(path, ".jsonl") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	// A pi session file is sessions/<project>/<file>.jsonl, so "sessions" must
	// be followed by at least a project dir and the file: index <= len-3.
	underSessions := false
	for i := 0; i <= len(parts)-3; i++ {
		if parts[i] == "sessions" {
			underSessions = true
			if i > 0 && parts[i-1] == "agent" {
				// ~/.pi/agent/sessions and PI_CODING_AGENT_DIR/sessions: the
				// "agent" parent is distinctive enough to skip the sniff.
				return true
			}
		}
	}
	if !underSessions {
		return false
	}
	return piHeaderSniff(path)
}

// piHeaderSniff reports whether the first non-empty line of path is a pi
// session header ({"type":"session",...}). It reads at most one line.
func piHeaderSniff(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, initialBufferSize), MaxLineSize)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var h struct {
			Type string `json:"type"`
		}
		return json.Unmarshal([]byte(line), &h) == nil && h.Type == "session"
	}
	return false
}

// ReadPiFile reads a pi (pi-coding-agent) JSONL session file and returns its
// normalized entries. It is exported for callers that already know a file is a
// pi session and want to bypass the path-based detection in [ReadFile].
func ReadPiFile(ctx context.Context, path string) ([]Entry, error) {
	return readPiFile(ctx, path)
}

func readPiFile(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines, err := readPiLines(ctx, f)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("pi session empty: %s", path)
	}

	var header piSessionHeader
	if err := json.Unmarshal(lines[0], &header); err != nil || header.Type != "session" {
		return nil, fmt.Errorf("pi session missing header: %s", path)
	}
	sessionID := header.ID
	if sessionID == "" {
		return nil, fmt.Errorf("pi session missing id: %s", path)
	}

	version := ""
	if header.Version != 0 {
		version = fmt.Sprintf("%d", header.Version)
	}
	entries := []Entry{{
		Type:      "session_meta",
		Subtype:   "session_meta",
		SessionID: sessionID,
		Timestamp: parsePiTime(header.Timestamp),
		CWD:       header.CWD,
		Version:   version,
		Source:    "pi",
	}}

	for _, line := range lines[1:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var pe piEntry
		if json.Unmarshal(line, &pe) != nil {
			continue
		}
		if pe.Type != "message" || pe.Message == nil {
			// model_change, thinking_level_change, compaction, label, and
			// other control entries carry no transcript content; skip them.
			continue
		}
		entry, ok := piMessageEntry(sessionID, header, version, pe)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func piMessageEntry(sessionID string, header piSessionHeader, version string, pe piEntry) (Entry, bool) {
	msg := pe.Message
	blocks := piBlocks(msg)
	if len(blocks) == 0 {
		return Entry{}, false
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		return Entry{}, false
	}

	role := msg.Role
	if role == "toolResult" {
		// Normalize tool results to a user-role message carrying a tool_result
		// block, matching how Claude Code records them, so ToolResults() and
		// IsToolResultOnly() recognize them.
		role = "user"
	}

	var usage *Usage
	if msg.Usage != nil {
		usage = &Usage{
			InputTokens:              msg.Usage.Input,
			OutputTokens:             msg.Usage.Output,
			CacheReadInputTokens:     msg.Usage.CacheRead,
			CacheCreationInputTokens: msg.Usage.CacheWrite,
		}
	}

	return Entry{
		Type:       role,
		SessionID:  sessionID,
		UUID:       pe.ID,
		ParentUUID: pe.ParentID,
		Timestamp:  parsePiTime(pe.Timestamp),
		CWD:        header.CWD,
		Version:    version,
		Source:     "pi",
		Usage:      usage,
		Message: &Message{
			ID:         pe.ID,
			Role:       role,
			Content:    raw,
			Model:      msg.Model,
			StopReason: msg.StopReason,
			Usage:      usage,
		},
	}, true
}

// piBlocks converts a pi message into canonical content blocks. Tool-result
// messages (role "toolResult") become a single tool_result block; ordinary
// messages map text/thinking/toolCall/image blocks.
func piBlocks(msg *piMessage) []ContentBlock {
	if msg.Role == "toolResult" {
		content := strings.TrimSpace(piContentText(msg.Content))
		// Drop a tool result that carries nothing useful (no id, no content,
		// no error) rather than emit an empty block.
		if content == "" && msg.ToolCallID == "" && !msg.IsError {
			return nil
		}
		return []ContentBlock{{
			Type:      "tool_result",
			ToolUseID: msg.ToolCallID,
			Content:   content,
			IsError:   msg.IsError,
		}}
	}

	var raw []piBlock
	if json.Unmarshal(msg.Content, &raw) != nil {
		// Content may be a bare string for user/custom messages.
		if text := piContentText(msg.Content); text != "" {
			return []ContentBlock{{Type: "text", Text: text}}
		}
		return nil
	}

	var blocks []ContentBlock
	for _, b := range raw {
		switch b.Type {
		case "text":
			if b.Text != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				blocks = append(blocks, ContentBlock{Type: "text", Text: b.Thinking})
			}
		case "toolCall":
			blocks = append(blocks, ContentBlock{
				Type:  "tool_use",
				ID:    b.ID,
				Name:  piToolName(b.Name),
				Input: b.Arguments,
			})
		case "image":
			blocks = append(blocks, ContentBlock{
				Type:      "image",
				Data:      b.Data,
				MediaType: b.MimeType,
			})
		}
	}
	return blocks
}

// piContentText extracts text from a content value that may be a bare string
// or a block array of text blocks (used by user and toolResult messages).
func piContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []piBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "thinking":
			if b.Thinking != "" {
				parts = append(parts, b.Thinking)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// piToolName maps pi's lowercase built-in tool names to the capitalized names
// the rest of cc uses, leaving custom tool names untouched.
func piToolName(name string) string {
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
	case "glob", "find":
		return "Glob"
	case "ls":
		return "LS"
	default:
		return name
	}
}

// readPiLines reads the non-empty JSONL lines of a pi session file, using the
// same generous line buffer cc applies to other session files.
func readPiLines(ctx context.Context, f *os.File) ([]json.RawMessage, error) {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, initialBufferSize), MaxLineSize)

	var lines []json.RawMessage
	n := 0
	for sc.Scan() {
		n++
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		line := make([]byte, len(b))
		copy(line, b)
		lines = append(lines, line)
	}
	return lines, sc.Err()
}

func parsePiTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}
