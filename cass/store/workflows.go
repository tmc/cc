package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
	// PhasesJSON is the JSON-encoded []cass.WorkflowPhase declared by the script.
	PhasesJSON string `json:"phases_json"`
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
		phases_json TEXT NOT NULL DEFAULT '[]',
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
	query := workflowsSelect()
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
	return scanWorkflowRows(rows)
}

// WorkflowsSince returns indexed workflow runs that started at or after the
// given time (zero time returns all), ordered by start time then run id.
func (s *DB) WorkflowsSince(ctx context.Context, since time.Time) ([]WorkflowRow, error) {
	query := workflowsSelect()
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
	return scanWorkflowRows(rows)
}

// WorkflowsByParentIDs returns workflow runs for the given parent sessions,
// grouped by parent session ID and ordered by workflow start time then run ID.
func (s *DB) WorkflowsByParentIDs(ctx context.Context, parentSessionIDs []string) (map[string][]WorkflowRow, error) {
	if len(parentSessionIDs) == 0 {
		return map[string][]WorkflowRow{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(parentSessionIDs)), ",")
	args := make([]any, len(parentSessionIDs))
	for i, id := range parentSessionIDs {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT parent_session_id, run_id, task_id, name, description, status, summary,
			script_path, transcript_dir, source_path, agent_count, journal_event_count,
			started_at, completed_at, phases_json, agents_json
		FROM workflows WHERE parent_session_id IN (`+placeholders+`)
		ORDER BY started_at, run_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query workflows by parent ids: %w", err)
	}
	defer rows.Close()
	out := map[string][]WorkflowRow{}
	for rows.Next() {
		var w WorkflowRow
		if err := rows.Scan(
			&w.ParentSessionID, &w.RunID, &w.TaskID, &w.Name, &w.Description, &w.Status, &w.Summary,
			&w.ScriptPath, &w.TranscriptDir, &w.SourcePath, &w.AgentCount, &w.JournalEventCount,
			&w.StartedAt, &w.CompletedAt, &w.PhasesJSON, &w.AgentsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan workflow by parent ids: %w", err)
		}
		out[w.ParentSessionID] = append(out[w.ParentSessionID], w)
	}
	return out, rows.Err()
}

// WorkflowCount returns the number of indexed workflow runs.
func (s *DB) WorkflowCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflows`).Scan(&n)
	return n, err
}

func workflowsSelect() string {
	return `
		SELECT parent_session_id, run_id, task_id, name, description, status, summary,
			script_path, transcript_dir, source_path, agent_count, journal_event_count,
			started_at, completed_at, phases_json, agents_json
		FROM workflows`
}

func scanWorkflowRows(rows *sql.Rows) ([]WorkflowRow, error) {
	var out []WorkflowRow
	for rows.Next() {
		var w WorkflowRow
		if err := rows.Scan(
			&w.ParentSessionID, &w.RunID, &w.TaskID, &w.Name, &w.Description, &w.Status, &w.Summary,
			&w.ScriptPath, &w.TranscriptDir, &w.SourcePath, &w.AgentCount, &w.JournalEventCount,
			&w.StartedAt, &w.CompletedAt, &w.PhasesJSON, &w.AgentsJSON,
		); err != nil {
			return nil, fmt.Errorf("scan workflow: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}
