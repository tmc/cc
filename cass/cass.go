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

// SearchRequest encapsulates query parameters.
type SearchRequest struct {
	Query   string
	Mode    SearchMode
	Filters Filters
	Limit   int
	Offset  int
}

// Filters constrains search results.
type Filters struct {
	Agent     string    // Filter by agent slug.
	Workspace string    // Filter by workspace path.
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
	FilesEdited  int `json:"files_edited,omitempty"`
	LinesWritten int `json:"lines_written,omitempty"`
	DurationSecs int    `json:"duration_secs,omitempty"`
	Sparkline    string `json:"sparkline,omitempty"`
	IT2Sends     int    `json:"it2_sends,omitempty"`
	IT2Screens   int `json:"it2_screens,omitempty"`
	IT2Splits    int `json:"it2_splits,omitempty"`
}

// SessionLink represents an interaction between two iTerm2 sessions.
// Links are categorized into two kinds:
//   - Messages: send-text, send-key (active communication from source to target)
//   - Observations: get-screen, get-buffer (source reading target's state)
type SessionLink struct {
	SourceSession string `json:"source_session"` // iTerm2 session ID of the acting session.
	TargetSession string `json:"target_session"` // iTerm2 session ID of the target.
	Kind          string `json:"kind"`            // "message" or "observation".
	Action        string `json:"action"`          // "send-text", "send-key", "get-screen", or "get-buffer".
	Text          string `json:"text,omitempty"`  // Content (for send-text/send-key).
	Timestamp     string `json:"timestamp,omitempty"`
}

// SessionStats holds extracted metrics for a session.
type SessionStats struct {
	// Tool usage.
	ToolCalls    int            `json:"tool_calls"`
	ToolBreakdown map[string]int `json:"tool_breakdown,omitempty"` // Tool name -> count.

	// Token usage.
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	CacheReads   int `json:"cache_reads"`

	// Code metrics.
	FilesRead    int `json:"files_read"`
	FilesWritten int `json:"files_written"`
	FilesEdited  int `json:"files_edited"`
	LinesWritten int `json:"lines_written"` // Approximate lines from Write/Edit.

	// Session metrics.
	Turns          int `json:"turns"`            // User message count.
	DurationSecs   int `json:"duration_secs"`
	SubagentSpawns int `json:"subagent_spawns"`

	// it2 interactions.
	IT2Splits  int `json:"it2_splits"`
	IT2Sends   int `json:"it2_sends"`   // send-text + send-key.
	IT2Screens int `json:"it2_screens"` // get-screen.
	IT2Buffers int `json:"it2_buffers"` // get-buffer.
	IT2Badges  int `json:"it2_badges"`  // set-badge.
	IT2Watches int `json:"it2_watches"` // watch.

	// Team interactions (claude teams infrastructure).
	TeamInboxReads  int `json:"team_inbox_reads"`  // Inbox message reads.
	TeamInboxSends  int `json:"team_inbox_sends"`  // Inbox message sends.
	TeamTaskOps     int `json:"team_task_ops"`      // Task create/update/list.
	TeamSpawns      int `json:"team_spawns"`        // Agent spawns via ccspawn.

	// Activity sparkline: message counts bucketed into time slots.
	// Encoded as a string of Unicode block chars (▁▂▃▄▅▆▇█).
	Sparkline string `json:"sparkline,omitempty"`
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
