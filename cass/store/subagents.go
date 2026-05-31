package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

// SubagentRunFilter constrains a SubagentRuns listing.
type SubagentRunFilter struct {
	ParentSessionID    string
	Workspace          string
	GitCommonDir       string
	Model              string
	AgentType          string
	ExcludeCompactions bool
	Limit              int
	Offset             int
}

// SubagentRunListEntry is one row from SubagentRuns: a SubagentRun joined
// with the parent session's title for display.
type SubagentRunListEntry struct {
	cass.SubagentRun
	ParentTitle         string `json:"parent_title,omitempty"`
	AgentDefName        string `json:"agent_def_name,omitempty"`
	AgentDefDescription string `json:"agent_def_description,omitempty"`
	AgentDefSourcePath  string `json:"agent_def_source_path,omitempty"`
	AgentDefDisabled    bool   `json:"agent_def_disabled,omitempty"`
}

// SubagentRuns lists subagent runs ordered by started_at DESC.
func (s *DB) SubagentRuns(ctx context.Context, f SubagentRunFilter) ([]SubagentRunListEntry, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}

	var where []string
	var args []any
	if f.ParentSessionID != "" {
		where = append(where, "r.parent_session_id = ?")
		args = append(args, f.ParentSessionID)
	}
	if f.Workspace != "" {
		where = append(where, "r.workspace LIKE ?")
		args = append(args, "%"+f.Workspace+"%")
	}
	if f.GitCommonDir != "" {
		where = append(where, "r.git_common_dir = ?")
		args = append(args, f.GitCommonDir)
	}
	if f.Model != "" {
		where = append(where, "r.model = ?")
		args = append(args, f.Model)
	}
	if f.AgentType != "" {
		where = append(where, "r.agent_type = ?")
		args = append(args, f.AgentType)
	}
	if f.ExcludeCompactions {
		where = append(where, "r.is_compaction = 0")
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT r.parent_session_id, r.agent_id, r.parent_claude_sid, r.workspace, r.git_common_dir,
			r.agent_type, r.description, r.model,
			r.enqueued_at, r.dequeued_at, r.started_at, r.ended_at,
			r.status, r.tool_use_id, r.output_file, r.worktree_path, r.worktree_branch,
			r.total_tokens, r.tool_uses, r.duration_ms, r.entry_count,
			r.source_path, r.meta_path, r.is_compaction,
			COALESCE(s.title, ''),
			COALESCE(ad.name, ''), COALESCE(ad.description, ''),
			COALESCE(ad.source_path, ''), COALESCE(ad.disabled, 0)
		FROM subagent_runs r
		LEFT JOIN sessions s ON s.id = r.parent_session_id
		LEFT JOIN agent_defs ad ON ad.name = r.agent_type
		%s
		ORDER BY r.started_at DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("subagent_runs: %w", err)
	}
	defer rows.Close()

	var out []SubagentRunListEntry
	for rows.Next() {
		var (
			e                                        SubagentRunListEntry
			enqUnix, deqUnix, startedUnix, endedUnix int64
			isCompaction                             int
			agentDefDisabled                         int
		)
		if err := rows.Scan(
			&e.ParentSessionID, &e.AgentID, &e.ParentClaudeSID, &e.Workspace, &e.GitCommonDir,
			&e.AgentType, &e.Description, &e.Model,
			&enqUnix, &deqUnix, &startedUnix, &endedUnix,
			&e.Status, &e.ToolUseID, &e.OutputFile, &e.WorktreePath, &e.WorktreeBranch,
			&e.TotalTokens, &e.ToolUses, &e.DurationMs, &e.EntryCount,
			&e.SourcePath, &e.MetaPath, &isCompaction,
			&e.ParentTitle,
			&e.AgentDefName, &e.AgentDefDescription, &e.AgentDefSourcePath, &agentDefDisabled,
		); err != nil {
			return nil, err
		}
		e.EnqueuedAt = unixOrZero(enqUnix)
		e.DequeuedAt = unixOrZero(deqUnix)
		e.StartedAt = unixOrZero(startedUnix)
		e.EndedAt = unixOrZero(endedUnix)
		e.IsCompaction = isCompaction != 0
		e.AgentDefDisabled = agentDefDisabled != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// SubagentRunSummary aggregates run-level stats across the index.
type SubagentRunSummary struct {
	TotalRuns        int
	SessionsWithRuns int
	TotalTokens      int64
	TotalDurationMs  int64
	ByAgentType      map[string]int
}

// SubagentRunsSummary returns aggregate counts and per-agent-type
// histogram. Used by `cass stats`.
func (s *DB) SubagentRunsSummary(ctx context.Context) (SubagentRunSummary, error) {
	var sum SubagentRunSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COUNT(DISTINCT parent_session_id),
			COALESCE(SUM(total_tokens), 0), COALESCE(SUM(duration_ms), 0)
		FROM subagent_runs
	`).Scan(&sum.TotalRuns, &sum.SessionsWithRuns, &sum.TotalTokens, &sum.TotalDurationMs)
	if err != nil {
		return sum, fmt.Errorf("subagent_runs summary: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(agent_type, ''), '(unknown)') AS k, COUNT(*)
		FROM subagent_runs
		GROUP BY k
		ORDER BY COUNT(*) DESC
	`)
	if err != nil {
		return sum, fmt.Errorf("subagent_runs by agent_type: %w", err)
	}
	defer rows.Close()
	sum.ByAgentType = map[string]int{}
	for rows.Next() {
		var k string
		var v int
		if err := rows.Scan(&k, &v); err != nil {
			return sum, err
		}
		sum.ByAgentType[k] = v
	}
	return sum, rows.Err()
}

// GraphSubagentRow is the lightweight form of a subagent run used to build
// graph nodes and edges: just the parent link, identity, and display fields.
type GraphSubagentRow struct {
	ParentSessionID     string
	AgentID             string
	AgentType           string
	Description         string
	AgentDefName        string
	AgentDefDescription string
	AgentDefSourcePath  string
	AgentDefDisabled    bool
	Model               string
	Status              string
	StartedAt           int64
	TotalTokens         int
}

// GraphSubagents returns every indexed subagent run as a graph row, ordered by
// start time then agent id for deterministic output. Compaction subagents are
// excluded; they are bookkeeping, not real fan-out.
func (s *DB) GraphSubagents(ctx context.Context) ([]GraphSubagentRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.parent_session_id, r.agent_id, r.agent_type, r.description,
			COALESCE(ad.name, ''), COALESCE(ad.description, ''),
			COALESCE(ad.source_path, ''), COALESCE(ad.disabled, 0),
			r.model, r.status, r.started_at, r.total_tokens
		FROM subagent_runs r
		LEFT JOIN agent_defs ad ON ad.name = r.agent_type
		WHERE r.is_compaction = 0
		ORDER BY r.started_at, r.agent_id
	`)
	if err != nil {
		return nil, fmt.Errorf("query graph subagents: %w", err)
	}
	defer rows.Close()
	var out []GraphSubagentRow
	for rows.Next() {
		var r GraphSubagentRow
		var agentDefDisabled int
		if err := rows.Scan(
			&r.ParentSessionID, &r.AgentID, &r.AgentType, &r.Description,
			&r.AgentDefName, &r.AgentDefDescription, &r.AgentDefSourcePath, &agentDefDisabled,
			&r.Model, &r.Status, &r.StartedAt, &r.TotalTokens,
		); err != nil {
			return nil, fmt.Errorf("scan graph subagent: %w", err)
		}
		r.AgentDefDisabled = agentDefDisabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func unixOrZero(u int64) time.Time {
	// time.Time{}.Unix() == -62135596800 (Go zero value year-1).
	// Treat both 0 and the Go zero-value sentinel as "no timestamp."
	if u <= 0 {
		return time.Time{}
	}
	return time.Unix(u, 0).UTC()
}
