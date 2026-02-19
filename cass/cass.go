package cass

import (
	"context"
	"time"
)

// Session is the normalized representation of a coding session from any agent.
type Session struct {
	ID         string         `json:"id"`
	Agent      string         `json:"agent"`
	Title      string         `json:"title"`
	Workspace  string         `json:"workspace"`
	SourcePath string         `json:"source_path"`
	StartedAt  time.Time      `json:"started_at"`
	EndedAt    time.Time      `json:"ended_at"`
	Messages   []Message      `json:"messages"`
	Stats      SessionStats   `json:"stats"`
	Metadata   map[string]any `json:"metadata,omitempty"`

	// Agent teams context (native Claude Code teams).
	TeamName   string `json:"team_name,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	IsTeamLead bool   `json:"is_team_lead,omitempty"`
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

// SearchMode controls the type of search.
type SearchMode int

const (
	SearchLexical  SearchMode = iota // FTS5 full-text search.
	SearchSemantic                   // Reserved for future embedding search.
	SearchHybrid                     // Reserved for combined search.
)

// SortMode controls result ordering.
type SortMode string

const (
	SortRecent    SortMode = "recent"    // ended_at DESC (most recently active).
	SortRelevance SortMode = "relevance" // BM25 rank (only meaningful with a query).
	SortStarted   SortMode = "started"   // started_at DESC.
	SortOldest    SortMode = "oldest"    // started_at ASC.
)

// SearchRequest encapsulates query parameters.
type SearchRequest struct {
	Query   string
	Mode    SearchMode
	Sort    SortMode
	Filters Filters
	Limit   int
	Offset  int
}

// Filters constrains search results.
type Filters struct {
	Agent     string    // Filter by agent slug.
	Workspace string    // Filter by workspace path.
	Team      string    // Filter by agent team name.
	After     time.Time // Sessions started after this time.
	Before    time.Time // Sessions started before this time.
}

// SearchResult holds search results.
type SearchResult struct {
	Hits       []Hit `json:"hits"`
	TotalCount int   `json:"total_count"`
}

// Hit is a single search result.
type Hit struct {
	SessionID  string  `json:"session_id"`
	Agent      string  `json:"agent"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	Workspace  string  `json:"workspace,omitempty"`
	SourcePath string  `json:"source_path,omitempty"`
	StartedAt  string  `json:"started_at,omitempty"`
	EndedAt    string  `json:"ended_at,omitempty"`

	// Stats summary (populated when available).
	ToolCalls    int `json:"tool_calls,omitempty"`
	Turns        int `json:"turns,omitempty"`
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	CacheReads               int `json:"cache_reads,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	FilesEdited  int `json:"files_edited,omitempty"`
	LinesWritten int `json:"lines_written,omitempty"`
	DurationSecs int    `json:"duration_secs,omitempty"`
	Sparkline    string `json:"sparkline,omitempty"`
	Compactions  int    `json:"compactions,omitempty"`
	IT2Sends     int    `json:"it2_sends,omitempty"`
	IT2Screens   int `json:"it2_screens,omitempty"`
	IT2Splits    int `json:"it2_splits,omitempty"`

	ToolBreakdown map[string]int `json:"tool_breakdown,omitempty"`

	// Agent teams context.
	TeamName   string `json:"team_name,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	IsTeamLead bool   `json:"is_team_lead,omitempty"`
}

// SessionLink represents an interaction between two sessions.
// Links are categorized into kinds:
//   - Messages: send-text, send-key (active communication from source to target)
//   - Observations: get-screen, get-buffer (source reading target's state)
//   - Team: team-spawn, team-message (native Claude Code agent teams)
type SessionLink struct {
	SourceSession string `json:"source_session"`          // iTerm2 session ID or agent name.
	TargetSession string `json:"target_session"`          // iTerm2 session ID or agent name.
	Kind          string `json:"kind"`                    // "message", "observation", or "team".
	Action        string `json:"action"`                  // "send-text", "team-spawn", "team-message", etc.
	Text          string `json:"text,omitempty"`          // Content excerpt.
	Timestamp     string `json:"timestamp,omitempty"`
	TeamName      string `json:"team_name,omitempty"`     // Team name for team links.
}

// SessionStats holds extracted metrics for a session.
type SessionStats struct {
	// Tool usage.
	ToolCalls    int            `json:"tool_calls"`
	ToolBreakdown map[string]int `json:"tool_breakdown,omitempty"` // Tool name -> count.

	// Token usage.
	// NOTE: OutputTokens is systematically undercounted: JSONL assistant entries store the
	// streaming-start snapshot (output_tokens=1) not the final count. The final output token
	// count is only available in SSE message_delta events which are not persisted to JSONL.
	// Real output token usage is typically 10-100x higher than what is reported here.
	//
	// NOTE: Hidden Haiku classifier calls (~294 input + 21 output tokens per user turn) are
	// made by Claude Code before each Sonnet response and are completely absent from JSONL.
	// Actual token usage is ~3% higher than JSONL-derived figures.
	InputTokens              int  `json:"input_tokens"`
	OutputTokens             int  `json:"output_tokens"`
	OutputTokensEstimated    bool `json:"output_tokens_estimated,omitempty"` // True when output tokens estimated via BPE (JSONL lacks final counts).
	CacheReads               int  `json:"cache_reads"`
	CacheCreationInputTokens int  `json:"cache_creation_input_tokens"`

	// Code metrics.
	FilesRead    int `json:"files_read"`
	FilesWritten int `json:"files_written"`
	FilesEdited  int `json:"files_edited"`
	LinesWritten int `json:"lines_written"` // Approximate lines from Write/Edit.

	// Session metrics.
	Turns          int `json:"turns"`            // User message count.
	PlanModeTurns  int `json:"plan_mode_turns"`  // User turns with permissionMode=plan.
	DurationSecs   int `json:"duration_secs"`
	SubagentSpawns int `json:"subagent_spawns"`
	Compactions    int `json:"compactions"`      // Context compaction count.

	// it2 interactions.
	IT2Splits  int `json:"it2_splits"`
	IT2Sends   int `json:"it2_sends"`   // send-text + send-key.
	IT2Screens int `json:"it2_screens"` // get-screen.
	IT2Buffers int `json:"it2_buffers"` // get-buffer.
	IT2Badges  int `json:"it2_badges"`  // set-badge.
	IT2Watches int `json:"it2_watches"` // watch.

	// Team interactions (claude teams infrastructure).
	TeamInboxReads     int `json:"team_inbox_reads"`      // Inbox message reads.
	TeamInboxSends     int `json:"team_inbox_sends"`      // Inbox message sends.
	TeamTaskOps        int `json:"team_task_ops"`          // Task create/update/list.
	TeamSpawns         int `json:"team_spawns"`            // Agent spawns via ccspawn.
	TeamMembersSpawned int `json:"team_members_spawned"`   // Members spawned via Task with team_name.
	TeamMessagesRecvd  int `json:"team_messages_received"` // Incoming <teammate-message> count.

	// Activity sparkline: message counts bucketed into time slots.
	// Encoded as a string of Unicode block chars (▁▂▃▄▅▆▇█).
	Sparkline string `json:"sparkline,omitempty"`
}

// GraphData holds combined node and link data for the session graph.
type GraphData struct {
	Nodes     []GraphNode   `json:"nodes"`
	Links     []SessionLink `json:"links"`
	TimeRange TimeRange     `json:"time_range"`
}

// GraphNode represents a session in the communication graph.
type GraphNode struct {
	ID        string `json:"id"`         // iTerm2 session ID (short prefix).
	Workspace string `json:"workspace"`
	Title     string `json:"title"`
	StartedAt int64  `json:"started_at,omitempty"`
	ToolCalls int    `json:"tool_calls,omitempty"`
	Turns     int    `json:"turns,omitempty"`
	Tokens    int    `json:"tokens,omitempty"` // input + output.
	IsActive  bool   `json:"is_active"`
	TeamName  string `json:"team_name,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
}

// TimeRange is the min/max timestamp range for graph data.
type TimeRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}

// DeleteFilter specifies which sessions to remove.
type DeleteFilter struct {
	IDs   []string // Delete specific session IDs.
	Agent string   // Delete all sessions from an agent.
}

// Index defines the interface for session storage and search.
type Index interface {
	// BatchIndex adds or updates sessions atomically.
	BatchIndex(ctx context.Context, sessions []Session) error

	// Search executes a query and returns matching results.
	Search(ctx context.Context, req SearchRequest) (*SearchResult, error)

	// Delete removes sessions matching the filter.
	Delete(ctx context.Context, filter DeleteFilter) error

	// Close releases underlying resources.
	Close() error
}

// APIRequest represents a single HTTP request/response to the Anthropic API,
// extracted from a HAR file captured by a proxy like Proxyman.
//
// HAR data provides ground truth for token usage that JSONL session files
// cannot: accurate output tokens (JSONL stores streaming-start snapshot),
// hidden classifier calls (Haiku topic detection), cache creation tokens,
// rate-limit utilization snapshots, and context composition breakdown.
type APIRequest struct {
	ID        string `json:"id"`         // SHA256(request_id + timestamp).
	SessionID string `json:"session_id"` // Linked cass session ID (may be empty).
	RequestID string `json:"request_id"` // x-request-id from response headers.
	Timestamp int64  `json:"timestamp"`  // Request time (unix seconds).

	// Model routing.
	Model       string `json:"model"`        // Full model ID (e.g. "claude-sonnet-4-6").
	ModelFamily string `json:"model_family"` // Normalized: "sonnet", "haiku", "opus".
	Purpose     string `json:"purpose"`      // "response", "classifier", "compact", "unknown".

	// Token usage (final values from SSE message_delta).
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`

	// Context breakdown (byte sizes from request body).
	SystemPromptBytes   int `json:"system_prompt_bytes"`
	ToolDefinitionBytes int `json:"tool_definition_bytes"`
	ConversationBytes   int `json:"conversation_bytes"`
	TotalRequestBytes   int `json:"total_request_bytes"`

	// Rate-limit snapshot from response headers.
	RateLimits RateLimitSnapshot `json:"rate_limits"`

	// Response metadata.
	StatusCode int    `json:"status_code"`
	StopReason string `json:"stop_reason"`
	DurationMs int    `json:"duration_ms"`

	// Source tracking for deduplication.
	SourceFile string `json:"source_file"`
	SourceHash string `json:"source_hash"` // SHA256 of HAR entry content.

	// iTerm2 session linkage (populated from artifact dir path).
	IT2SessionID string `json:"it2_session_id,omitempty"` // UUID from ~/.it2/sessions/<uuid>/
	ClientPID    int    `json:"client_pid,omitempty"`      // PID from proxy-traffic.<pid>.jsonl

	// Detailed context breakdown (populated by ParseContextBreakdown; not stored in DB).
	// Available in-memory after parsing; use for display and per-session aggregation.
	Breakdown *ContextBreakdown `json:"breakdown,omitempty"`
}

// RateLimitSnapshot captures rate-limit utilization at a point in time,
// extracted from anthropic-ratelimit-unified-* response headers.
type RateLimitSnapshot struct {
	Timestamp int64 `json:"timestamp"` // When this snapshot was taken (unix seconds).

	// Global buckets.
	Utilization5h float64 `json:"utilization_5h"`
	Reset5h       int64   `json:"reset_5h"`
	Utilization7d float64 `json:"utilization_7d"`
	Reset7d       int64   `json:"reset_7d"`

	// Per-model sub-buckets (present for Sonnet, Opus; absent for Haiku).
	ModelBucket      string  `json:"model_bucket,omitempty"`      // e.g. "7d_sonnet".
	ModelUtilization float64 `json:"model_utilization,omitempty"`
	ModelReset       int64   `json:"model_reset,omitempty"`

	RepresentativeClaim string `json:"representative_claim,omitempty"` // "five_hour" or "seven_day".
}

// ContextBreakdown measures the composition of an API request body.
// Tool definitions and system prompts dominate context usage: in a typical
// Claude Code session, tools[] is ~73% of the request, system[] ~17%, and
// the actual conversation messages[] only ~10%.
//
// The per-tool and per-block breakdowns allow attributing context cost to
// specific sources: which MCP server is most expensive, how large is CLAUDE.md,
// what fraction of context is consumed by tool definitions vs conversation.
type ContextBreakdown struct {
	// Coarse byte counts (always populated from raw JSON lengths).
	SystemPromptBytes   int `json:"system_prompt_bytes"`
	ToolDefinitionBytes int `json:"tool_definition_bytes"`
	ConversationBytes   int `json:"conversation_bytes"`
	TotalRequestBytes   int `json:"total_request_bytes"`

	// Counts.
	SystemBlockCount int `json:"system_block_count"`
	ToolCount        int `json:"tool_count"`
	MessageCount     int `json:"message_count"`

	// Per-tool breakdown (populated by ParseContextBreakdown).
	// Key: tool name (e.g. "Bash", "mcp__posthog__query-run").
	ToolBytes map[string]int `json:"tool_bytes,omitempty"`

	// Tool category summaries (populated by ParseContextBreakdown).
	BuiltinToolBytes int `json:"builtin_tool_bytes,omitempty"` // Claude Code built-ins.
	MCPToolBytes     int `json:"mcp_tool_bytes,omitempty"`     // All mcp__* tools combined.
	SkillToolBytes   int `json:"skill_tool_bytes,omitempty"`   // Skill-injected tools.

	// Per-MCP-server breakdown.
	// Key: server name extracted from mcp__<server>__<tool> (e.g. "posthog").
	MCPServerBytes map[string]int `json:"mcp_server_bytes,omitempty"`

	// System block attribution (populated by ParseContextBreakdown).
	// Key: block kind ("claude_md", "skill", "tool_result", "text", "unknown").
	SystemBlockBytes map[string]int `json:"system_block_bytes,omitempty"`
}

// ToolEntry is a single tool definition from the tools[] array.
// Used internally by ParseContextBreakdown; not stored directly.
type ToolEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// BuiltinTools is the set of Claude Code built-in tool names.
// These are always present; their cost is fixed per version.
var BuiltinTools = map[string]bool{
	"Bash": true, "Read": true, "Write": true, "Edit": true,
	"Glob": true, "Grep": true, "Task": true, "WebFetch": true,
	"WebSearch": true, "NotebookEdit": true, "TodoWrite": true,
	"TodoRead": true, "AskUserQuestion": true, "ExitPlanMode": true,
	"EnterPlanMode": true, "Skill": true, "LSP": true,
	"ToolSearch": true, "ListMcpResourcesTool": true, "ReadMcpResourceTool": true,
}
