package store

import (
	"context"
	"fmt"
	"time"
)

// WorkflowRow is the indexed form of a native Claude Code workflow run
// (cass.WorkflowRun). ParentSessionID joins back to sessions.id. Rows are
// cleared and reinserted per parent session at index time, mirroring
// subagent_runs.
type WorkflowRow struct {
	ParentSessionID   string `json:"parent_session_id"`
	RunID             string `json:"run_id"`
	TaskID            string `json:"task_id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	Status            string `json:"status"`
	Summary           string `json:"summary"`
	ScriptPath        string `json:"script_path"`
	TranscriptDir     string `json:"transcript_dir"`
	SourcePath        string `json:"source_path"`
	AgentCount        int    `json:"agent_count"`
	JournalEventCount int    `json:"journal_event_count"`
	StartedAt         int64  `json:"started_at"`
	CompletedAt       int64  `json:"completed_at"`
	// AgentsJSON is the JSON-encoded []cass.WorkflowAgent for this run; a small
	// bounded blob, mirroring goals_json/skills_json. Defaults to "[]".
	AgentsJSON string `json:"agents_json"`
}

const workflowsSchema = `
	CREATE TABLE IF NOT EXISTS workflows (
		parent_session_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		task_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		script_path TEXT NOT NULL DEFAULT '',
		transcript_dir TEXT NOT NULL DEFAULT '',
		source_path TEXT NOT NULL DEFAULT '',
		agent_count INTEGER NOT NULL DEFAULT 0,
		journal_event_count INTEGER NOT NULL DEFAULT 0,
		started_at INTEGER NOT NULL DEFAULT 0,
		completed_at INTEGER NOT NULL DEFAULT 0,
		indexed_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (parent_session_id, run_id),
		FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	CREATE INDEX IF NOT EXISTS idx_workflows_parent ON workflows(parent_session_id);
	CREATE INDEX IF NOT EXISTS idx_workflows_started ON workflows(started_at);
	CREATE INDEX IF NOT EXISTS idx_workflows_status ON workflows(status);
`

// Workflows returns indexed workflow runs, optionally restricted to one parent
// session, ordered by start time then run id for deterministic output.
func (s *DB) Workflows(ctx context.Context, parentSessionID string) ([]WorkflowRow, error) {
	query := `
		SELECT parent_session_id, run_id, task_id, name, description, status, summary,
			script_path, transcript_dir, source_path, agent_count, journal_event_count,
			started_at, completed_at, agents_json
		FROM workflows`
	var args []any
	if parentSessionID != "" {
		query += ` WHERE parent_session_id = ?`
		args = append(args, parentSessionID)
	}
	query += ` ORDER BY started_at, run_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query workflows: %w", err)
	}
	defer rows.Close()
	var out []WorkflowRow
	for rows.Next() {
		var w WorkflowRow
		if err := rows.Scan(
			&w.ParentSessionID, &w.RunID, &w.TaskID, &w.Name, &w.Description, &w.Status, &w.Summary,
			&w.ScriptPath, &w.TranscriptDir, &w.SourcePath, &w.AgentCount, &w.JournalEventCount,
			&w.StartedAt, &w.CompletedAt, &w.AgentsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkflowsSince returns indexed workflow runs that started at or after the
// given time (zero time returns all), ordered by start time then run id.
func (s *DB) WorkflowsSince(ctx context.Context, since time.Time) ([]WorkflowRow, error) {
	query := `
		SELECT parent_session_id, run_id, task_id, name, description, status, summary,
			script_path, transcript_dir, source_path, agent_count, journal_event_count,
			started_at, completed_at, agents_json
		FROM workflows`
	var args []any
	if !since.IsZero() {
		query += ` WHERE started_at >= ?`
		args = append(args, since.Unix())
	}
	query += ` ORDER BY started_at, run_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query workflows since: %w", err)
	}
	defer rows.Close()
	var out []WorkflowRow
	for rows.Next() {
		var w WorkflowRow
		if err := rows.Scan(
			&w.ParentSessionID, &w.RunID, &w.TaskID, &w.Name, &w.Description, &w.Status, &w.Summary,
			&w.ScriptPath, &w.TranscriptDir, &w.SourcePath, &w.AgentCount, &w.JournalEventCount,
			&w.StartedAt, &w.CompletedAt, &w.AgentsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkflowCount returns the number of indexed workflow runs.
func (s *DB) WorkflowCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflows`).Scan(&n)
	return n, err
}
