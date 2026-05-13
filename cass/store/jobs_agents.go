package store

import (
	"context"
	"fmt"
	"time"
)

// Job is the indexed form of a daemon-backed Claude Code job (~/.claude/jobs/<shortId>).
// A job wraps a Session: SessionID joins back to sessions.id when the session has
// been indexed. State and timeline fields support search and dashboarding.
type Job struct {
	ShortID         string `json:"short_id"`
	SessionID       string `json:"session_id"`
	ResumeSessionID string `json:"resume_session_id"`
	Name            string `json:"name"`
	NameSource      string `json:"name_source"`
	Intent          string `json:"intent"`
	State           string `json:"state"`
	Detail          string `json:"detail"`
	Tempo           string `json:"tempo"`
	InFlightTasks   int    `json:"in_flight_tasks"`
	InFlightQueued  int    `json:"in_flight_queued"`
	Template        string `json:"template"`
	Backend         string `json:"backend"`
	CLIVersion      string `json:"cli_version"`
	CWD             string `json:"cwd"`
	OriginCWD       string `json:"origin_cwd"`
	LinkScanPath    string `json:"link_scan_path"`
	LinkScanOffset  int64  `json:"link_scan_offset"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
	FirstTerminalAt int64  `json:"first_terminal_at"`
	EventCount      int    `json:"event_count"`
	OutputResult    string `json:"output_result"`
	SourcePath      string `json:"source_path"`
}

// AgentDef is the indexed form of a user-defined agent template
// (~/.claude/agents/<name>.json). Disabled defs live under .disabled/.
type AgentDef struct {
	Name             string `json:"name"`
	Description      string `json:"description"`
	Version          string `json:"version"`
	Command          string `json:"command"`
	Disabled         bool   `json:"disabled"`
	KeywordsJSON     string `json:"keywords_json"`
	PatternsJSON     string `json:"patterns_json"`
	ToolsJSON        string `json:"tools_json"`
	CapabilitiesJSON string `json:"capabilities_json"`
	SourcePath       string `json:"source_path"`
	Searchable       string `json:"searchable"`
}

const jobsAgentsSchema = `
	CREATE TABLE IF NOT EXISTS jobs (
		short_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL DEFAULT '',
		resume_session_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		name_source TEXT NOT NULL DEFAULT '',
		intent TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		tempo TEXT NOT NULL DEFAULT '',
		in_flight_tasks INTEGER NOT NULL DEFAULT 0,
		in_flight_queued INTEGER NOT NULL DEFAULT 0,
		template TEXT NOT NULL DEFAULT '',
		backend TEXT NOT NULL DEFAULT '',
		cli_version TEXT NOT NULL DEFAULT '',
		cwd TEXT NOT NULL DEFAULT '',
		origin_cwd TEXT NOT NULL DEFAULT '',
		link_scan_path TEXT NOT NULL DEFAULT '',
		link_scan_offset INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL DEFAULT 0,
		first_terminal_at INTEGER NOT NULL DEFAULT 0,
		event_count INTEGER NOT NULL DEFAULT 0,
		output_result TEXT NOT NULL DEFAULT '',
		source_path TEXT NOT NULL DEFAULT '',
		indexed_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_jobs_session ON jobs(session_id);
	CREATE INDEX IF NOT EXISTS idx_jobs_state ON jobs(state);
	CREATE INDEX IF NOT EXISTS idx_jobs_updated ON jobs(updated_at);

	CREATE TABLE IF NOT EXISTS agent_defs (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL DEFAULT '',
		version TEXT NOT NULL DEFAULT '',
		command TEXT NOT NULL DEFAULT '',
		disabled INTEGER NOT NULL DEFAULT 0,
		keywords_json TEXT NOT NULL DEFAULT '[]',
		patterns_json TEXT NOT NULL DEFAULT '[]',
		tools_json TEXT NOT NULL DEFAULT '[]',
		capabilities_json TEXT NOT NULL DEFAULT '[]',
		source_path TEXT NOT NULL DEFAULT '',
		searchable TEXT NOT NULL DEFAULT '',
		indexed_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_agent_defs_disabled ON agent_defs(disabled);
`

// SaveJob upserts a Job row.
func (s *Store) SaveJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO jobs
			(short_id, session_id, resume_session_id, name, name_source, intent,
			 state, detail, tempo, in_flight_tasks, in_flight_queued,
			 template, backend, cli_version, cwd, origin_cwd,
			 link_scan_path, link_scan_offset,
			 created_at, updated_at, first_terminal_at,
			 event_count, output_result, source_path, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ShortID, j.SessionID, j.ResumeSessionID, j.Name, j.NameSource, j.Intent,
		j.State, j.Detail, j.Tempo, j.InFlightTasks, j.InFlightQueued,
		j.Template, j.Backend, j.CLIVersion, j.CWD, j.OriginCWD,
		j.LinkScanPath, j.LinkScanOffset,
		j.CreatedAt, j.UpdatedAt, j.FirstTerminalAt,
		j.EventCount, j.OutputResult, j.SourcePath, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save job %s: %w", j.ShortID, err)
	}
	return nil
}

// Jobs returns indexed jobs ordered by updated_at desc.
func (s *Store) Jobs(ctx context.Context) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT short_id, session_id, resume_session_id, name, name_source, intent,
			state, detail, tempo, in_flight_tasks, in_flight_queued,
			template, backend, cli_version, cwd, origin_cwd,
			link_scan_path, link_scan_offset,
			created_at, updated_at, first_terminal_at,
			event_count, output_result, source_path
		FROM jobs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(
			&j.ShortID, &j.SessionID, &j.ResumeSessionID, &j.Name, &j.NameSource, &j.Intent,
			&j.State, &j.Detail, &j.Tempo, &j.InFlightTasks, &j.InFlightQueued,
			&j.Template, &j.Backend, &j.CLIVersion, &j.CWD, &j.OriginCWD,
			&j.LinkScanPath, &j.LinkScanOffset,
			&j.CreatedAt, &j.UpdatedAt, &j.FirstTerminalAt,
			&j.EventCount, &j.OutputResult, &j.SourcePath,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// JobCount returns the number of indexed jobs.
func (s *Store) JobCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM jobs`).Scan(&n)
	return n, err
}

// SaveAgentDef upserts an AgentDef row.
func (s *Store) SaveAgentDef(ctx context.Context, a AgentDef) error {
	disabled := 0
	if a.Disabled {
		disabled = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO agent_defs
			(name, description, version, command, disabled,
			 keywords_json, patterns_json, tools_json, capabilities_json,
			 source_path, searchable, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Name, a.Description, a.Version, a.Command, disabled,
		a.KeywordsJSON, a.PatternsJSON, a.ToolsJSON, a.CapabilitiesJSON,
		a.SourcePath, a.Searchable, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save agent def %s: %w", a.Name, err)
	}
	return nil
}

// AgentDefs returns indexed agent definitions ordered by name.
func (s *Store) AgentDefs(ctx context.Context) ([]AgentDef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, description, version, command, disabled,
			keywords_json, patterns_json, tools_json, capabilities_json,
			source_path, searchable
		FROM agent_defs ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query agent defs: %w", err)
	}
	defer rows.Close()
	var out []AgentDef
	for rows.Next() {
		var a AgentDef
		var disabled int
		if err := rows.Scan(
			&a.Name, &a.Description, &a.Version, &a.Command, &disabled,
			&a.KeywordsJSON, &a.PatternsJSON, &a.ToolsJSON, &a.CapabilitiesJSON,
			&a.SourcePath, &a.Searchable,
		); err != nil {
			return nil, fmt.Errorf("scan agent def: %w", err)
		}
		a.Disabled = disabled != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentDefCount returns the number of indexed agent definitions.
func (s *Store) AgentDefCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_defs`).Scan(&n)
	return n, err
}
