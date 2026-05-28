package cass

import (
	"context"
	"time"
)

// Session is the normalized representation of a coding session from any agent.
type Session struct {
	ID           string         `json:"id"`
	Agent        string         `json:"agent"`
	Title        string         `json:"title"`
	Workspace    string         `json:"workspace"`
	GitCommonDir string         `json:"git_common_dir,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	SourcePath   string         `json:"source_path"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      time.Time      `json:"ended_at"`
	Messages     []Message      `json:"messages"`
	Goals        []Goal         `json:"goals,omitempty"`
	Skills       []SkillUse     `json:"skills,omitempty"`
	Workflows    []WorkflowRun  `json:"workflows,omitempty"`
	Stats        SessionStats   `json:"stats"`
	Metadata     map[string]any `json:"metadata,omitempty"`

	// Agent teams context (native Claude Code teams).
	TeamName   string `json:"team_name,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	IsTeamLead bool   `json:"is_team_lead,omitempty"`

	// Subagents are Task tool invocations spawned by this session that
	// produced their own JSONL under <sessionId>/subagents/. Populated by
	// the collector; persisted by the store as a separate subagent_runs row
	// keyed on (session_id, agent_id).
	Subagents []SubagentRun `json:"subagents,omitempty"`
}

// Message is a single message within a session.
type Message struct {
	ID        string    `json:"id,omitempty"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Snippets  []Snippet `json:"snippets,omitempty"`
}

// Snippet is a code reference within a message.
type Snippet struct {
	FilePath  string `json:"file_path"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Content   string `json:"content,omitempty"`
	Language  string `json:"language,omitempty"`
}

// DetectionResult reports whether an agent's data is present.
type DetectionResult struct {
	Agent string   `json:"agent"`
	Found bool     `json:"found"`
	Paths []string `json:"paths,omitempty"`
}

// ScanConfig controls how a collector scans for sessions.
type ScanConfig struct {
	Paths   []string  // Root paths to scan.
	Since   time.Time // Only include sessions modified after this time.
	Project string    // Filter to a specific project substring.
}

// Collector discovers and parses session logs from an agent.
type Collector interface {
	// Name returns the agent slug (e.g. "claude-code").
	Name() string

	// Detect checks if the agent's data is present on the system.
	Detect(ctx context.Context) (*DetectionResult, error)

	// Scan walks root paths and sends found sessions to out.
	// It respects ScanConfig for incremental indexing.
	Scan(ctx context.Context, config ScanConfig, out chan<- Session) error
}

// SessionStats holds extracted metrics for a session.
type SessionStats struct {
	// Tool usage.
	ToolCalls     int            `json:"tool_calls"`
	ToolBreakdown map[string]int `json:"tool_breakdown,omitempty"` // Tool name -> count.

	// Token usage. OutputTokensSnapshot is the streaming-start value persisted in
	// JSONL (often 1); real output usage is typically 10-100x higher and only
	// available from SSE message_delta events. Hidden Haiku classifier calls
	// (~294 in + 21 out per user turn) are absent from JSONL entirely.
	InputTokens              int  `json:"input_tokens"`
	OutputTokensSnapshot     int  `json:"output_tokens"`
	OutputTokensEstimated    bool `json:"output_tokens_estimated,omitempty"` // True when output tokens estimated via BPE (JSONL lacks final counts).
	CacheReads               int  `json:"cache_reads"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens"`

	// Code metrics.
	FilesRead    int `json:"files_read"`
	FilesWritten int `json:"files_written"`
	FilesEdited  int `json:"files_edited"`
	LinesWritten int `json:"lines_written"` // Approximate lines from Write/Edit.

	// Session metrics.
	Turns          int `json:"turns"`           // User message count.
	PlanModeTurns  int `json:"plan_mode_turns"` // User turns with permissionMode=plan.
	DurationSecs   int `json:"duration_secs"`
	SubagentSpawns int `json:"subagent_spawns"`
	Compactions    int `json:"compactions"` // Context compaction count.

	// it2 interactions.
	IT2Splits  int `json:"it2_splits"`
	IT2Sends   int `json:"it2_sends"`   // send-text + send-key.
	IT2Screens int `json:"it2_screens"` // get-screen.
	IT2Buffers int `json:"it2_buffers"` // get-buffer.
	IT2Badges  int `json:"it2_badges"`  // set-badge.
	IT2Watches int `json:"it2_watches"` // watch.

	// Team interactions (claude teams infrastructure).
	TeamInboxReads     int `json:"team_inbox_reads"`       // Inbox message reads.
	TeamInboxSends     int `json:"team_inbox_sends"`       // Inbox message sends.
	TeamTaskOps        int `json:"team_task_ops"`          // Task create/update/list.
	TeamSpawns         int `json:"team_spawns"`            // Agent spawns via ccspawn.
	TeamMembersSpawned int `json:"team_members_spawned"`   // Members spawned via Task with team_name.
	TeamMessagesRecvd  int `json:"team_messages_received"` // Incoming <teammate-message> count.

	// Native Claude Code workflows and task checklist tools.
	WorkflowRuns            int `json:"workflow_runs"`
	WorkflowAsyncRuns       int `json:"workflow_async_runs,omitempty"`
	WorkflowAgentRuns       int `json:"workflow_agent_runs,omitempty"`
	WorkflowKeywordRequests int `json:"workflow_keyword_requests,omitempty"`
	WorkflowTaskOps         int `json:"workflow_task_ops,omitempty"`
	TaskCreates             int `json:"task_creates,omitempty"`
	TaskUpdates             int `json:"task_updates,omitempty"`

	// Activity sparkline: message counts bucketed into time slots.
	// Encoded as a string of Unicode block chars (▁▂▃▄▅▆▇█).
	Sparkline string `json:"sparkline,omitempty"`
}

// DeleteFilter specifies which sessions to remove.
type DeleteFilter struct {
	IDs   []string // Delete specific session IDs.
	Agent string   // Delete all sessions from an agent.
}
