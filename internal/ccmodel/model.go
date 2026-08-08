// Package ccmodel holds the normalized session data model shared by the cc
// package and its internal per-format readers (codexread, opencoderead,
// piread). It depends on nothing else in the module, so the readers can
// construct [Entry] values without importing cc and creating an import cycle.
//
// The cc package re-exports these types (type Entry = ccmodel.Entry, etc.), so
// callers continue to use cc.Entry, cc.Message, and the rest; this package is
// an implementation detail and is not meant to be imported directly.
package ccmodel

import (
	"bytes"
	"encoding/json"
	"time"
)

// Line-buffer sizes shared by every reader. Tool-result payloads can be large,
// so the scanner buffer grows up to MaxLineSize.
const (
	InitialBufferSize = 256 * 1024
	MaxLineSize       = 10 * 1024 * 1024
)

// Entry is a single line in a Claude Code JSONL session file.
type Entry struct {
	Type       string    `json:"type"`
	SessionID  string    `json:"sessionId,omitempty"`
	UUID       string    `json:"uuid,omitempty"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	Originator string    `json:"originator,omitempty"`
	Source     string    `json:"source,omitempty"`
	Phase      string    `json:"phase,omitempty"`

	// User/assistant message fields.
	ParentUUID  string   `json:"parentUuid,omitempty"`
	LeafUUID    string   `json:"leafUuid,omitempty"`
	IsSidechain bool     `json:"isSidechain,omitempty"`
	UserType    string   `json:"userType,omitempty"`
	CWD         string   `json:"cwd,omitempty"`
	Version     string   `json:"version,omitempty"`
	GitBranch   string   `json:"gitBranch,omitempty"`
	Slug        string   `json:"slug,omitempty"`
	Message     *Message `json:"message,omitempty"`

	// Less-common JSONL fields preserved for callers that need request and
	// entry-linkage details beyond the normalized Message.
	ThinkingMetadata        json.RawMessage `json:"thinkingMetadata,omitempty"`
	Todos                   json.RawMessage `json:"todos,omitempty"`
	RequestID               string          `json:"requestId,omitempty"`
	SourceToolAssistantUUID string          `json:"sourceToolAssistantUUID,omitempty"`
	Usage                   *Usage          `json:"usage,omitempty"`

	// Custom title set via /rename command.
	CustomTitle string `json:"customTitle,omitempty"`

	// Subtype distinguishes entry variants (e.g. "compact_boundary").
	Subtype string `json:"subtype,omitempty"`

	// Summary fields.
	Summary          string           `json:"summary,omitempty"`
	IsCompactSummary bool             `json:"isCompactSummary,omitempty"`
	CompactMetadata  *CompactMetadata `json:"compactMetadata,omitempty"`

	// Visibility hints.
	IsVisibleInTranscriptOnly bool `json:"isVisibleInTranscriptOnly,omitempty"`
	IsMeta                    bool `json:"isMeta,omitempty"`

	// IsAPIErrorMessage marks a synthetic assistant message carrying an API
	// error (Claude Code writes these with model "<synthetic>"; other
	// readers set it when the harness records an error field).
	IsAPIErrorMessage bool `json:"isApiErrorMessage,omitempty"`

	// Agent teams fields.
	TeamName  string `json:"teamName,omitempty"`
	AgentName string `json:"agentName,omitempty"`

	// Subagent fields (entries inside subagents/<uuid>/ directories).
	AgentID string `json:"agentId,omitempty"` // UUID of the subagent instance.

	// Codex subagent linkage, captured from a spawned session's session_meta
	// (source.subagent.thread_spawn). ParentThreadID is the spawning session's
	// id; AgentNickname and AgentRole describe the spawned agent.
	ParentThreadID string `json:"parentThreadId,omitempty"`
	AgentNickname  string `json:"agentNickname,omitempty"`
	AgentRole      string `json:"agentRole,omitempty"`

	// Permission mode for the turn (e.g. "plan", "default").
	PermissionMode string `json:"permissionMode,omitempty"`

	// Progress fields.
	Operation  string          `json:"operation,omitempty"` // queue-operation variant (enqueue, dequeue, remove, popAll).
	Content    string          `json:"content,omitempty"`
	Level      string          `json:"level,omitempty"`
	DurationMs int             `json:"durationMs,omitempty"` // turn_duration system entries
	Data       json.RawMessage `json:"data,omitempty"`

	// Tool result fields.
	ToolUseResult *ToolUseResult `json:"toolUseResult,omitempty"`

	// File history snapshot.
	Snapshot         json.RawMessage `json:"snapshot,omitempty"`
	IsSnapshotUpdate bool            `json:"isSnapshotUpdate,omitempty"`

	// Attachment carries typed event payloads emitted by the CLI itself
	// (not the model). Known types include "goal_status", "skill_listing",
	// "task_reminder", "command_permissions", "deferred_tools_delta",
	// "edited_text_file", "queued_command".
	Attachment *Attachment `json:"attachment,omitempty"`
}

// Attachment is a CLI-emitted event payload attached to an entry.
// The Type field tags the variant; type-specific fields are populated when
// recognized, and Raw preserves the original JSON for forward compatibility.
type Attachment struct {
	Type string `json:"type"`

	// goal_status fields. See https://go.dev/doc/comment — these are zero
	// when the attachment is a different variant.
	Condition string `json:"condition,omitempty"`
	Met       bool   `json:"met,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Sentinel  bool   `json:"sentinel,omitempty"`

	// Raw is the full original attachment JSON, preserved for variants we
	// do not yet have typed fields for.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON keeps the raw payload alongside the typed decode so callers
// can recover fields specific to less common attachment variants.
func (a *Attachment) UnmarshalJSON(data []byte) error {
	type alias Attachment
	var v alias
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*a = Attachment(v)
	a.Raw = append(a.Raw[:0], data...)
	return nil
}

// MarshalJSON restores fields retained in Raw and overlays the typed fields.
// This preserves attachment variants that this version does not yet know.
func (a Attachment) MarshalJSON() ([]byte, error) {
	type alias Attachment
	typed, err := json.Marshal(alias(a))
	if err != nil || len(a.Raw) == 0 {
		return typed, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(a.Raw, &fields); err != nil {
		return nil, err
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(typed, &known); err != nil {
		return nil, err
	}
	for name, value := range known {
		fields[name] = value
	}
	return json.Marshal(fields)
}

// Message is the core message object with role and content.
type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Model      string          `json:"model,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
}

// ContentBlock is a single block within a message's content array.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Path      string          `json:"path,omitempty"`
	FilePath  string          `json:"file_path,omitempty"`
	ImageURL  string          `json:"image_url,omitempty"`
	URL       string          `json:"url,omitempty"`
	Data      string          `json:"data,omitempty"`
	MIMEType  string          `json:"mime_type,omitempty"`
	MediaType string          `json:"media_type,omitempty"`

	// ContentRaw holds the original content field when it is an array of
	// nested blocks rather than a string. Content then carries the text
	// extracted from it, which is empty for payloads that hold no text.
	ContentRaw json.RawMessage `json:"-"`
}

// UnmarshalJSON accepts a content field that is either a string or an array of
// nested blocks. Claude Code writes the array form for structured tool results,
// such as the tool_reference lists returned by ToolSearch. Because a message's
// blocks are decoded as one array, a single such block would otherwise fail the
// decode and discard every block in the message.
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	type alias ContentBlock
	var v struct {
		alias
		Content json.RawMessage `json:"content,omitempty"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*b = ContentBlock(v.alias)
	raw := bytes.TrimLeft(v.Content, " \t\r\n")
	switch {
	case len(raw) == 0:
	case raw[0] == '"':
		if err := json.Unmarshal(raw, &b.Content); err != nil {
			return err
		}
	default:
		b.ContentRaw = append(b.ContentRaw[:0], raw...)
		b.Content = ExtractAnyText(raw)
	}
	return nil
}

// Usage holds token usage information.
type Usage struct {
	InputTokens              int                  `json:"input_tokens"`
	OutputTokens             int                  `json:"output_tokens"`
	CacheReadInputTokens     int                  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int                  `json:"cache_creation_input_tokens,omitempty"`
	ServiceTier              string               `json:"service_tier,omitempty"`
	CacheCreation            *CacheCreationDetail `json:"cache_creation,omitempty"`
}

// CacheCreationDetail holds ephemeral cache token counts.
type CacheCreationDetail struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// CompactMetadata accompanies compact summary entries.
type CompactMetadata struct {
	PreTokens int    `json:"preTokens,omitempty"`
	Trigger   string `json:"trigger,omitempty"`
}

// PersistedOutputPrefix begins the placeholder Claude Code inlines in place of
// a tool result it spilled to a sidecar file named by
// [ToolUseResult.PersistedOutputPath].
const PersistedOutputPrefix = "<persisted-output>"

// ToolUseResult holds the result of a tool invocation.
type ToolUseResult struct {
	Type    string          `json:"type,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`

	// Text holds the result when Claude Code writes toolUseResult as a bare
	// string rather than an object, which it does for tool errors and user
	// rejections ("User rejected tool use").
	Text string `json:"-"`

	Stdout   string      `json:"stdout,omitempty"`
	Stderr   string      `json:"stderr,omitempty"`
	Status   string      `json:"status,omitempty"`
	Success  bool        `json:"success,omitempty"`
	FilePath string      `json:"filePath,omitempty"`
	File     *FileResult `json:"file,omitempty"`
	Error    string      `json:"error,omitempty"`

	// PersistedOutputPath names the sidecar file Claude Code writes when a
	// tool result is too large to inline. The transcript then carries only a
	// short preview, so a reader that trusts the inline text sees a fraction
	// of the output; PersistedOutputSize is the sidecar's size in bytes.
	PersistedOutputPath string `json:"persistedOutputPath,omitempty"`
	PersistedOutputSize int    `json:"persistedOutputSize,omitempty"`

	// Edit fields.
	OldString string `json:"oldString,omitempty"`
	NewString string `json:"newString,omitempty"`

	// Search fields.
	NumMatches int `json:"numMatches,omitempty"`
	NumFiles   int `json:"numFiles,omitempty"`

	// AskUserQuestion fields: the questions asked and the user's answers,
	// keyed by question text.
	Questions []AskedQuestion   `json:"questions,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`

	// Task fields.
	Task   *TaskResult   `json:"task,omitempty"`
	Tasks  []TaskSummary `json:"tasks,omitempty"`
	TaskID string        `json:"taskId,omitempty"`

	// Workflow fields returned by Claude Code's Workflow tool.
	RunID         string `json:"runId,omitempty"`
	Summary       string `json:"summary,omitempty"`
	TranscriptDir string `json:"transcriptDir,omitempty"`
	ScriptPath    string `json:"scriptPath,omitempty"`

	// Artifact fields returned by Claude Code's Artifact tool. URL is the
	// published claude.ai artifact link, Path the local source file, and
	// Title the artifact's title.
	URL   string `json:"url,omitempty"`
	Path  string `json:"path,omitempty"`
	Title string `json:"title,omitempty"`

	// Usage for subagent results.
	Usage             *Usage  `json:"usage,omitempty"`
	DurationMs        float64 `json:"durationMs,omitempty"`
	TotalDurationMs   float64 `json:"totalDurationMs,omitempty"`
	TotalTokens       int     `json:"totalTokens,omitempty"`
	TotalToolUseCount int     `json:"totalToolUseCount,omitempty"`
}

// UnmarshalJSON accepts every shape Claude Code writes for toolUseResult: an
// object, a bare string, or a content-block array. A strict object decode
// fails on the latter two, discarding the entry that carries them.
func (t *ToolUseResult) UnmarshalJSON(data []byte) error {
	*t = ToolUseResult{}
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return nil
	}
	switch trimmed[0] {
	case '"':
		return json.Unmarshal(trimmed, &t.Text)
	case '[':
		t.Content = append(t.Content[:0], trimmed...)
		return nil
	}
	type alias ToolUseResult
	var v alias
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return err
	}
	*t = ToolUseResult(v)
	return nil
}

// FileResult holds file read results.
type FileResult struct {
	FilePath   string `json:"filePath,omitempty"`
	Content    string `json:"content,omitempty"`
	NumLines   int    `json:"numLines,omitempty"`
	StartLine  int    `json:"startLine,omitempty"`
	TotalLines int    `json:"totalLines,omitempty"`
}

// AskedQuestion is one question from an AskUserQuestion invocation,
// preserving the order questions were asked in.
type AskedQuestion struct {
	Question string `json:"question,omitempty"`
	Header   string `json:"header,omitempty"`
}

// TaskResult holds task tool results.
type TaskResult struct {
	ID          string `json:"id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	TaskType    string `json:"task_type,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    *int   `json:"exitCode,omitempty"`
}

// TaskSummary is a brief task entry from TaskList results.
type TaskSummary struct {
	ID        string   `json:"id,omitempty"`
	Subject   string   `json:"subject,omitempty"`
	Status    string   `json:"status,omitempty"`
	BlockedBy []string `json:"blockedBy,omitempty"`
}
