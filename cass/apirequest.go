package cass

// APIRequest represents a single HTTP request/response to the Anthropic API,
// extracted from a HAR file captured by a proxy like Proxyman.
//
// HAR data provides ground truth for token usage that JSONL session files
// cannot: accurate output tokens (JSONL stores streaming-start snapshot),
// hidden classifier calls (Haiku topic detection), cache creation tokens,
// rate-limit utilization snapshots, and context composition breakdown.
type APIRequest struct {
	ID        string `json:"id"`         // SHA256(request_id + timestamp).
	SessionID string `json:"session_id"` // Claude session UUID; legacy rows may hold cass session ID.
	RequestID string `json:"request_id"` // x-request-id from response headers.
	Timestamp int64  `json:"timestamp"`  // Request time (unix seconds).
	Method    string `json:"method"`     // HTTP method used for the request.

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
	ClientPID    int    `json:"client_pid,omitempty"`     // PID from proxy-traffic.<pid>.jsonl

	// Identity fields extracted from metadata.user_id and response headers.
	// metadata.user_id format: "user_<hash>_account_<uuid>_session_<uuid>"
	// Account is the billing entity; it can change mid-session on account switch.
	// OrgID comes from the x-organization-id response header (per-request ground truth).
	UserHash    string `json:"user_hash,omitempty"`    // Opaque user hash from metadata.user_id.
	AccountUUID string `json:"account_uuid,omitempty"` // Account UUID from metadata.user_id.
	OrgID       string `json:"org_id,omitempty"`       // Organization ID from response headers.

	// Detailed context breakdown (populated by ParseContextBreakdown; not stored in DB).
	// Available in-memory after parsing; use for display and per-session aggregation.
	Breakdown *ContextBreakdown `json:"breakdown,omitempty"`
}

// APIRequestFilter constrains API request listings.
type APIRequestFilter struct {
	SessionID   string
	Since       int64
	ModelFamily string
	Purpose     string
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
	ModelBucket      string  `json:"model_bucket,omitempty"` // e.g. "7d_sonnet".
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
	BuiltinToolBytes int      `json:"builtin_tool_bytes,omitempty"` // Claude Code built-ins.
	MCPToolBytes     int      `json:"mcp_tool_bytes,omitempty"`     // All mcp__* tools combined.
	SkillToolBytes   int      `json:"skill_tool_bytes,omitempty"`   // Skill-injected tools.
	SkillNames       []string `json:"skill_names,omitempty"`        // Skill names observed in request context.

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
