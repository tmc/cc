package cass

import "time"

// SubagentRun is one Task subagent invocation. Subagent JSONLs share the
// parent's Claude sessionId, so a SubagentRun is a sibling node to Session,
// not a child Session.
//
// Token totals come from the parent's queue-operation <task-notification>
// usage block, which is authoritative — the per-message output_tokens in
// JSONL is always 1 (streaming-start snapshot). When a notification is
// missing (lost on crash, very old logs), Status is "unknown" and the
// usage fields stay zero.
type SubagentRun struct {
	AgentID         string    `json:"agent_id"`          // From agent-<agentId>.jsonl filename.
	ParentSessionID string    `json:"parent_session_id"` // cass.Session.ID (sha256-derived).
	ParentClaudeSID string    `json:"parent_claude_sid"` // Claude sessionId.
	Workspace       string    `json:"workspace,omitempty"`
	GitCommonDir    string    `json:"git_common_dir,omitempty"`
	AgentType       string    `json:"agent_type,omitempty"`  // From meta.json or Task tool input.
	Description     string    `json:"description,omitempty"` // From meta.json.
	Model           string    `json:"model,omitempty"`       // From assistant entries or Task tool input.
	EnqueuedAt      time.Time `json:"enqueued_at"`           // Notification posted to queue.
	DequeuedAt      time.Time `json:"dequeued_at"`           // Parent consumed notification.
	StartedAt       time.Time `json:"started_at"`            // First entry in subagent JSONL.
	EndedAt         time.Time `json:"ended_at"`              // Last entry in subagent JSONL.
	Status          string    `json:"status,omitempty"`      // completed | error | unknown.
	ToolUseID       string    `json:"tool_use_id,omitempty"` // Parent tool_use that spawned this run.
	OutputFile      string    `json:"output_file,omitempty"`
	WorktreePath    string    `json:"worktree_path,omitempty"`
	WorktreeBranch  string    `json:"worktree_branch,omitempty"`
	TotalTokens     int       `json:"total_tokens,omitempty"`
	ToolUses        int       `json:"tool_uses,omitempty"`
	DurationMs      int64     `json:"duration_ms,omitempty"`
	EntryCount      int       `json:"entry_count,omitempty"`
	SourcePath      string    `json:"source_path,omitempty"`   // Subagent JSONL absolute path.
	MetaPath        string    `json:"meta_path,omitempty"`     // meta.json sidecar path (may be empty).
	IsCompaction    bool      `json:"is_compaction,omitempty"` // True for agent-acompact-*.
}
