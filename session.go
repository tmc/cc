package cc

import (
	"encoding/json"
	"time"
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

	// Agent teams fields.
	TeamName  string `json:"teamName,omitempty"`
	AgentName string `json:"agentName,omitempty"`

	// Subagent fields (entries inside subagents/<uuid>/ directories).
	AgentID string `json:"agentId,omitempty"` // UUID of the subagent instance.

	// Permission mode for the turn (e.g. "plan", "default").
	PermissionMode string `json:"permissionMode,omitempty"`

	// Progress fields.
	Content string          `json:"content,omitempty"`
	Level   string          `json:"level,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`

	// Tool result fields.
	ToolUseResult *ToolUseResult `json:"toolUseResult,omitempty"`

	// File history snapshot.
	Snapshot         json.RawMessage `json:"snapshot,omitempty"`
	IsSnapshotUpdate bool            `json:"isSnapshotUpdate,omitempty"`
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

// ToolUseResult holds the result of a tool invocation.
type ToolUseResult struct {
	Type     string          `json:"type,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
	Stdout   string          `json:"stdout,omitempty"`
	Stderr   string          `json:"stderr,omitempty"`
	Status   string          `json:"status,omitempty"`
	Success  bool            `json:"success,omitempty"`
	FilePath string          `json:"filePath,omitempty"`
	File     *FileResult     `json:"file,omitempty"`
	Error    string          `json:"error,omitempty"`

	// Edit fields.
	OldString string `json:"oldString,omitempty"`
	NewString string `json:"newString,omitempty"`

	// Search fields.
	NumMatches int `json:"numMatches,omitempty"`
	NumFiles   int `json:"numFiles,omitempty"`

	// Task fields.
	Task   *TaskResult   `json:"task,omitempty"`
	Tasks  []TaskSummary `json:"tasks,omitempty"`
	TaskID string        `json:"taskId,omitempty"`

	// Usage for subagent results.
	Usage             *Usage  `json:"usage,omitempty"`
	DurationMs        float64 `json:"durationMs,omitempty"`
	TotalDurationMs   float64 `json:"totalDurationMs,omitempty"`
	TotalTokens       int     `json:"totalTokens,omitempty"`
	TotalToolUseCount int     `json:"totalToolUseCount,omitempty"`
}

// FileResult holds file read results.
type FileResult struct {
	FilePath   string `json:"filePath,omitempty"`
	Content    string `json:"content,omitempty"`
	NumLines   int    `json:"numLines,omitempty"`
	StartLine  int    `json:"startLine,omitempty"`
	TotalLines int    `json:"totalLines,omitempty"`
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
