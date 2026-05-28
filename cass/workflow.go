package cass

import "time"

// WorkflowRun is one native Claude Code Workflow invocation observed in a
// session. The parent session records the Workflow tool call; fan-out agents
// and the journal live under subagents/workflows/<run_id>/.
type WorkflowRun struct {
	RunID             string    `json:"run_id,omitempty"`
	TaskID            string    `json:"task_id,omitempty"`
	Name              string    `json:"name,omitempty"`
	Description       string    `json:"description,omitempty"`
	Status            string    `json:"status,omitempty"`
	Summary           string    `json:"summary,omitempty"`
	ScriptPath        string    `json:"script_path,omitempty"`
	TranscriptDir     string    `json:"transcript_dir,omitempty"`
	AgentCount        int       `json:"agent_count,omitempty"`
	JournalEventCount int       `json:"journal_event_count,omitempty"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	CompletedAt       time.Time `json:"completed_at,omitempty"`
	SourcePath        string    `json:"source_path,omitempty"`
}
