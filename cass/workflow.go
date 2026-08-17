package cass

import "time"

// WorkflowRun is one native Claude Code Workflow invocation observed in a
// session. The parent session records the Workflow tool call; fan-out agents
// and the journal live under subagents/workflows/<run_id>/.
type WorkflowRun struct {
	RunID             string          `json:"run_id,omitempty"`
	TaskID            string          `json:"task_id,omitempty"`
	Name              string          `json:"name,omitempty"`
	Description       string          `json:"description,omitempty"`
	Phases            []WorkflowPhase `json:"phases,omitempty"`
	Status            string          `json:"status,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	ScriptPath        string          `json:"script_path,omitempty"`
	TranscriptDir     string          `json:"transcript_dir,omitempty"`
	AgentCount        int             `json:"agent_count,omitempty"`
	JournalEventCount int             `json:"journal_event_count,omitempty"`
	StartedAt         time.Time       `json:"started_at,omitempty"`
	CompletedAt       time.Time       `json:"completed_at,omitempty"`
	SourcePath        string          `json:"source_path,omitempty"`

	// Model is the run's default model (state file defaultModel).
	Model string `json:"model,omitempty"`
	// DurationMs is the wall-clock run time reported by the state file.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// TotalTokens and TotalToolCalls are the run totals reported by the state
	// file. They cover every fan-out agent, including ones whose transcript has
	// been rotated away.
	TotalTokens    int `json:"total_tokens,omitempty"`
	TotalToolCalls int `json:"total_tool_calls,omitempty"`
	// Logs are the script's log() lines, in emission order.
	Logs []string `json:"logs,omitempty"`
	// Args is the JSON-encoded args value passed to the run, if any.
	Args string `json:"args,omitempty"`
	// Result is the JSON-encoded value the script returned, if any.
	Result string `json:"result,omitempty"`

	// Agents are the fan-out agent transcripts of this run, for tree rendering
	// and per-agent search attribution. They are never top-level sessions.
	Agents []WorkflowAgent `json:"agents,omitempty"`
}

// WorkflowPhase is one named phase declared by a native workflow script.
type WorkflowPhase struct {
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// WorkflowAgent is one fan-out agent transcript within a [WorkflowRun], living
// at <parent-uuid>/subagents/workflows/<run_id>/agent-<id>.jsonl. Its text is
// folded into the parent session's search content; its metadata hangs off the
// parent's WorkflowRun so the UI can render a tree and reach the transcript.
// Tokens reflect the JSONL streaming-start snapshot and are approximate.
type WorkflowAgent struct {
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	Phase      string `json:"phase,omitempty"`
	AgentType  string `json:"agent_type,omitempty"`
	Title      string `json:"title,omitempty"`
	ToolCalls  int    `json:"tool_calls,omitempty"`
	Tokens     int    `json:"tokens,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	Status     string `json:"status,omitempty"`

	// Index and PhaseIndex are the run-scoped ordinals the harness assigned.
	Index      int `json:"index,omitempty"`
	PhaseIndex int `json:"phase_index,omitempty"`
	// Model is the model this agent ran on; Attempt counts retries from 1.
	Model   string `json:"model,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	// DurationMs is the agent's wall-clock run time.
	DurationMs int64 `json:"duration_ms,omitempty"`
	// Isolation is set when the agent ran in its own git worktree; WorktreePath
	// is that worktree, from the agent's meta.json.
	Isolation    string `json:"isolation,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
	// SpawnDepth is the agent's nesting depth from the agent's meta.json.
	SpawnDepth int `json:"spawn_depth,omitempty"`
	// PromptPreview and ResultPreview are the harness-truncated prompt and
	// return value; LastTool* is the final tool the agent used.
	PromptPreview   string `json:"prompt_preview,omitempty"`
	ResultPreview   string `json:"result_preview,omitempty"`
	LastToolName    string `json:"last_tool_name,omitempty"`
	LastToolSummary string `json:"last_tool_summary,omitempty"`
	// CacheKey is the journal's resume key ("v2:<hash>") for this agent call,
	// which identifies a cached call across resumed runs.
	CacheKey string `json:"cache_key,omitempty"`
	// Result is the JSON-encoded value the agent returned, from the journal.
	Result string `json:"result,omitempty"`
}
