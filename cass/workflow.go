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
}
