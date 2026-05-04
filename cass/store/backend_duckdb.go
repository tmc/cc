//go:build duckdb

// DuckDB backend for cass session search.
//
// Build with: go build -tags duckdb ./...
// Requires CGO and the go-duckdb library:
//   go get github.com/marcboeker/go-duckdb
//
// DuckDB advantages over SQLite FTS5:
//   - Columnar storage compresses repeated text 5-10x better than row storage.
//   - The built-in FTS extension uses a separate inverted index with variable-
//     length integer encoding; no ~100x posting-list inflation.
//   - Appveyor benchmarks on 27 MB conversation text show DuckDB at ~80-120 MB
//     total vs SQLite at 8 GB (even with 32 KB FTS cap).
//
// Trade-offs:
//   - CGO required (no pure-Go path).
//   - DuckDB is OLAP-optimised; write latency is higher for small transactions.
//   - No snippet()/highlight() equivalent out of the box; we reconstruct from
//     the stored content column.

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/cc/cass"

	// go-duckdb registers as "duckdb" driver.
	_ "github.com/marcboeker/go-duckdb"
)

// duckBackend implements Backend using DuckDB + the FTS extension.
type duckBackend struct {
	db          *sql.DB
	maxFTSBytes int
}

func openDuckDB(cfg BackendConfig) (Backend, error) {
	path := cfg.Path
	if path == "" {
		path = ":memory:"
	}
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}

	// DuckDB FTS setup.
	for _, stmt := range []string{
		"INSTALL fts",
		"LOAD fts",
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", stmt, err)
		}
	}

	b := &duckBackend{db: db, maxFTSBytes: cfg.MaxFTSBytes}
	if err := b.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb migrate: %w", err)
	}
	return b, nil
}

func (b *duckBackend) migrate() error {
	// DuckDB uses PRAGMA CREATE INDEX ... USING fts for full-text search.
	// The base table is a standard columnar table; FTS is a secondary index.
	schema := `
		CREATE TABLE IF NOT EXISTS sessions (
			id VARCHAR PRIMARY KEY,
			agent VARCHAR NOT NULL DEFAULT '',
			title VARCHAR NOT NULL DEFAULT '',
			workspace VARCHAR NOT NULL DEFAULT '',
			source_path VARCHAR NOT NULL DEFAULT '',
			started_at BIGINT NOT NULL DEFAULT 0,
			ended_at BIGINT NOT NULL DEFAULT 0,
			content VARCHAR NOT NULL DEFAULT '',
			indexed_at BIGINT NOT NULL DEFAULT 0,
			tool_calls INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			files_edited INTEGER NOT NULL DEFAULT 0,
			lines_written INTEGER NOT NULL DEFAULT 0,
			turns INTEGER NOT NULL DEFAULT 0,
			duration_secs INTEGER NOT NULL DEFAULT 0,
			sparkline VARCHAR NOT NULL DEFAULT '',
			stats_json VARCHAR NOT NULL DEFAULT '{}',
			team_name VARCHAR NOT NULL DEFAULT '',
			agent_name VARCHAR NOT NULL DEFAULT '',
			is_team_lead BOOLEAN NOT NULL DEFAULT false,
			goal_count INTEGER NOT NULL DEFAULT 0,
			active_goal_count INTEGER NOT NULL DEFAULT 0,
			completed_goal_count INTEGER NOT NULL DEFAULT 0,
			goals_json VARCHAR NOT NULL DEFAULT '[]',
			skill_count INTEGER NOT NULL DEFAULT 0,
			selected_skill_count INTEGER NOT NULL DEFAULT 0,
			loaded_skill_count INTEGER NOT NULL DEFAULT 0,
			skills_json VARCHAR NOT NULL DEFAULT '[]'
		);
	`
	if _, err := b.db.Exec(schema); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// PRAGMA CREATE INDEX with fts builds the inverted index.
	// stem_language: 'english' enables Porter stemmer.
	// This index is stored alongside the DuckDB table and is significantly
	// more compact than SQLite's FTS5 posting lists.
	ftsCreate := `
		PRAGMA CREATE INDEX fts_idx ON sessions
		USING fts(title, content, agent)
		WITH (stem_language='english', stopwords='english');
	`
	if _, err := b.db.Exec(ftsCreate); err != nil {
		// Index may already exist on reconnect; treat as non-fatal.
		if !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("create fts index: %w", err)
		}
	}
	return nil
}

func (b *duckBackend) BatchIndex(ctx context.Context, sessions []cass.Session) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// DuckDB supports INSERT OR REPLACE via INSERT ... ON CONFLICT DO UPDATE.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sessions (
			id, agent, title, workspace, source_path,
			started_at, ended_at, content, indexed_at,
			tool_calls, input_tokens, output_tokens,
			files_edited, lines_written, turns, duration_secs,
			sparkline, stats_json, team_name, agent_name, is_team_lead,
			goal_count, active_goal_count, completed_goal_count, goals_json,
			skill_count, selected_skill_count, loaded_skill_count, skills_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			agent=excluded.agent, title=excluded.title,
			workspace=excluded.workspace, content=excluded.content,
			indexed_at=excluded.indexed_at,
			input_tokens=excluded.input_tokens, output_tokens=excluded.output_tokens,
			stats_json=excluded.stats_json,
			goal_count=excluded.goal_count,
			active_goal_count=excluded.active_goal_count,
			completed_goal_count=excluded.completed_goal_count,
			goals_json=excluded.goals_json,
			skill_count=excluded.skill_count,
			selected_skill_count=excluded.selected_skill_count,
			loaded_skill_count=excluded.loaded_skill_count,
			skills_json=excluded.skills_json
	`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, sess := range sessions {
		content := buildContentCapped(sess, b.maxFTSBytes)
		statsJSON, _ := json.Marshal(sess.Stats)
		goalsJSON, _ := json.Marshal(sess.Goals)
		skillsJSON, _ := json.Marshal(sess.Skills)
		goalCount, activeGoalCount, completedGoalCount := goalCounts(sess.Goals)
		skillCount, selectedSkillCount, loadedSkillCount := skillCounts(sess.Skills)
		if _, err := stmt.ExecContext(ctx,
			sess.ID, sess.Agent, sess.Title, sess.Workspace, sess.SourcePath,
			sess.StartedAt.Unix(), sess.EndedAt.Unix(), content, now,
			sess.Stats.ToolCalls, sess.Stats.InputTokens, sess.Stats.OutputTokens,
			sess.Stats.FilesEdited, sess.Stats.LinesWritten, sess.Stats.Turns, sess.Stats.DurationSecs,
			sess.Stats.Sparkline, string(statsJSON),
			sess.TeamName, sess.AgentName, sess.IsTeamLead,
			goalCount, activeGoalCount, completedGoalCount, string(goalsJSON),
			skillCount, selectedSkillCount, loadedSkillCount, string(skillsJSON),
		); err != nil {
			return fmt.Errorf("insert %s: %w", sess.ID, err)
		}
	}
	return tx.Commit()
}

func (b *duckBackend) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	var where []string
	var args []any

	if req.Query != "" {
		// DuckDB FTS uses match_bm25() which returns a relevance score.
		// We filter to non-NULL rows (no match → NULL).
		where = append(where, "match_bm25(id, ?) IS NOT NULL")
		args = append(args, req.Query)
	}
	if req.Filters.Agent != "" {
		where = append(where, agentFilter("agent"))
		args = append(args, agentFilterArgs(req.Filters.Agent)...)
	}
	if !req.Filters.After.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, req.Filters.After.Unix())
	}
	if !req.Filters.Before.IsZero() {
		where = append(where, "started_at <= ?")
		args = append(args, req.Filters.Before.Unix())
	}
	if req.Filters.Workspace != "" {
		where = append(where, "workspace LIKE ?")
		args = append(args, "%"+req.Filters.Workspace+"%")
	}
	if req.Filters.Team != "" {
		where = append(where, "team_name = ?")
		args = append(args, req.Filters.Team)
	}
	if req.Filters.GoalStatus != "" {
		where = append(where, "goals_json LIKE ?")
		args = append(args, `%"status":"`+req.Filters.GoalStatus+`"%`)
	}
	if req.Filters.Skill != "" {
		where = append(where, "skills_json LIKE ?")
		args = append(args, "%"+req.Filters.Skill+"%")
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	sort := req.Sort
	if sort == "" {
		if req.Query != "" {
			sort = cass.SortRelevance
		} else {
			sort = cass.SortRecent
		}
	}
	var orderClause string
	switch sort {
	case cass.SortRelevance:
		if req.Query != "" {
			orderClause = "ORDER BY match_bm25(id, ?) DESC"
			args = append(args, req.Query) // second bind for ORDER BY
		} else {
			orderClause = "ORDER BY ended_at DESC"
		}
	case cass.SortStarted:
		orderClause = "ORDER BY started_at DESC"
	case cass.SortOldest:
		orderClause = "ORDER BY started_at ASC"
	default:
		orderClause = "ORDER BY ended_at DESC"
	}

	query := fmt.Sprintf(`
		SELECT id, agent, title,
			CASE WHEN ? != '' THEN substr(content, 1, 300) ELSE substr(content, 1, 200) END as snip,
			COALESCE(match_bm25(id, ?), 0.0) as score,
			workspace, source_path, started_at, ended_at,
			tool_calls, turns, input_tokens, output_tokens,
			files_edited, lines_written, duration_secs,
			sparkline, stats_json, team_name, agent_name, is_team_lead,
			goals_json, goal_count, active_goal_count, completed_goal_count,
			skills_json, skill_count, selected_skill_count, loaded_skill_count
		FROM sessions
		%s %s LIMIT ? OFFSET ?`,
		whereClause, orderClause)

	// Prepend two args for the CASE/COALESCE query parameters.
	qargs := append([]any{req.Query, req.Query}, args...)
	qargs = append(qargs, req.Limit, req.Offset)

	rows, err := b.db.QueryContext(ctx, query, qargs...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var hits []cass.Hit
	for rows.Next() {
		var h cass.Hit
		var startedUnix, endedUnix int64
		var statsJSON, goalsJSON, skillsJSON string
		var isTeamLead bool
		if err := rows.Scan(
			&h.SessionID, &h.Agent, &h.Title, &h.Snippet, &h.Score,
			&h.Workspace, &h.SourcePath, &startedUnix, &endedUnix,
			&h.ToolCalls, &h.Turns, &h.InputTokens, &h.OutputTokens,
			&h.FilesEdited, &h.LinesWritten, &h.DurationSecs,
			&h.Sparkline, &statsJSON, &h.TeamName, &h.AgentName, &isTeamLead,
			&goalsJSON, &h.GoalCount, &h.ActiveGoalCount, &h.CompletedGoalCount,
			&skillsJSON, &h.SkillCount, &h.SelectedSkillCount, &h.LoadedSkillCount,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if startedUnix > 0 {
			h.StartedAt = time.Unix(startedUnix, 0).Format(time.RFC3339)
		}
		if endedUnix > 0 {
			h.EndedAt = time.Unix(endedUnix, 0).Format(time.RFC3339)
		}
		h.IsTeamLead = isTeamLead
		if statsJSON != "" {
			var stats cass.SessionStats
			if json.Unmarshal([]byte(statsJSON), &stats) == nil {
				h.ToolBreakdown = stats.ToolBreakdown
				h.Compactions = stats.Compactions
			}
		}
		if goalsJSON != "" {
			_ = json.Unmarshal([]byte(goalsJSON), &h.Goals)
		}
		if skillsJSON != "" {
			_ = json.Unmarshal([]byte(skillsJSON), &h.Skills)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	// Count total.
	var total int
	countArgs := args[:len(args)-0] // don't include LIMIT/OFFSET (not added yet)
	countQuery := fmt.Sprintf(`SELECT count(*) FROM sessions %s`, whereClause)
	if err := b.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		total = len(hits)
	}

	return &cass.SearchResult{Hits: hits, TotalCount: total}, nil
}

func (b *duckBackend) SessionCount(ctx context.Context) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n)
	return n, err
}

func (b *duckBackend) Close() error { return b.db.Close() }
