package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/cc/cass"

	_ "modernc.org/sqlite"
)

// DB implements cass.Index using SQLite with FTS5.
type DB struct {
	db *sql.DB
}

const hitStatsCols = `ended_at, tool_calls, turns, input_tokens, output_tokens, files_edited, lines_written, duration_secs, sparkline, subagent_spawns, it2_sends, it2_screens, it2_splits, stats_json, team_name, agent_name, is_team_lead, git_common_dir, branch, goals_json, goal_count, active_goal_count, completed_goal_count, skills_json, skill_count, selected_skill_count, loaded_skill_count`

type hitScanner interface {
	Scan(dest ...any) error
}

// New opens or creates a SQLite store at the given path.
func New(dbPath string) (*DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	// SQLite only supports one writer at a time. Limit the pool to avoid
	// holding idle connections that extend WAL checkpoints or cause SQLITE_BUSY.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(30 * time.Second)

	// Enable WAL mode for concurrent reads and busy timeout for contention.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}
	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *DB) migrate() error {
	schema := `
		CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			agent TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			workspace TEXT NOT NULL DEFAULT '',
			source_path TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL DEFAULT 0,
			ended_at INTEGER NOT NULL DEFAULT 0,
			content TEXT NOT NULL DEFAULT '',
			indexed_at INTEGER NOT NULL DEFAULT 0,
			tool_calls INTEGER NOT NULL DEFAULT 0,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			files_read INTEGER NOT NULL DEFAULT 0,
			files_written INTEGER NOT NULL DEFAULT 0,
			files_edited INTEGER NOT NULL DEFAULT 0,
			lines_written INTEGER NOT NULL DEFAULT 0,
			turns INTEGER NOT NULL DEFAULT 0,
			duration_secs INTEGER NOT NULL DEFAULT 0,
			subagent_spawns INTEGER NOT NULL DEFAULT 0,
			it2_splits INTEGER NOT NULL DEFAULT 0,
			it2_sends INTEGER NOT NULL DEFAULT 0,
			it2_screens INTEGER NOT NULL DEFAULT 0,
			it2_buffers INTEGER NOT NULL DEFAULT 0,
			team_inbox_reads INTEGER NOT NULL DEFAULT 0,
			team_inbox_sends INTEGER NOT NULL DEFAULT 0,
			team_task_ops INTEGER NOT NULL DEFAULT 0,
			team_spawns INTEGER NOT NULL DEFAULT 0,
			goal_count INTEGER NOT NULL DEFAULT 0,
			active_goal_count INTEGER NOT NULL DEFAULT 0,
			completed_goal_count INTEGER NOT NULL DEFAULT 0,
			goals_json TEXT NOT NULL DEFAULT '[]',
			skill_count INTEGER NOT NULL DEFAULT 0,
			selected_skill_count INTEGER NOT NULL DEFAULT 0,
			loaded_skill_count INTEGER NOT NULL DEFAULT 0,
			skills_json TEXT NOT NULL DEFAULT '[]',
			sparkline TEXT NOT NULL DEFAULT '',
			stats_json TEXT NOT NULL DEFAULT '{}'
		);

		CREATE TABLE IF NOT EXISTS session_links (
			session_id TEXT NOT NULL,
			source_session TEXT NOT NULL DEFAULT '',
			target_session TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			timestamp TEXT NOT NULL DEFAULT '',
			team_name TEXT NOT NULL DEFAULT '',
			FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_links_session ON session_links(session_id);
		CREATE INDEX IF NOT EXISTS idx_links_target ON session_links(target_session);

		CREATE TABLE IF NOT EXISTS session_mapping (
			iterm_session TEXT NOT NULL,
			claude_session TEXT NOT NULL,
			cass_session TEXT NOT NULL,
			workspace TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (iterm_session, claude_session)
		);

		CREATE TABLE IF NOT EXISTS metadata (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS team_configs (
			name TEXT PRIMARY KEY,
			lead_session_id TEXT NOT NULL DEFAULT '',
			lead_agent_id TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT 0,
			members_json TEXT NOT NULL DEFAULT '[]',
			indexed_at INTEGER NOT NULL DEFAULT 0
		);

		CREATE INDEX IF NOT EXISTS idx_team_lead_session ON team_configs(lead_session_id);

		CREATE TABLE IF NOT EXISTS api_requests (
			id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL DEFAULT '',
			request_id TEXT NOT NULL DEFAULT '',
			timestamp INTEGER NOT NULL DEFAULT 0,

			model TEXT NOT NULL DEFAULT '',
			model_family TEXT NOT NULL DEFAULT '',
			purpose TEXT NOT NULL DEFAULT '',

			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0,
			cache_creation_tokens INTEGER NOT NULL DEFAULT 0,

			system_prompt_bytes INTEGER NOT NULL DEFAULT 0,
			tool_definition_bytes INTEGER NOT NULL DEFAULT 0,
			conversation_bytes INTEGER NOT NULL DEFAULT 0,
			total_request_bytes INTEGER NOT NULL DEFAULT 0,

			rl_5h_utilization REAL NOT NULL DEFAULT 0,
			rl_5h_reset INTEGER NOT NULL DEFAULT 0,
			rl_7d_utilization REAL NOT NULL DEFAULT 0,
			rl_7d_reset INTEGER NOT NULL DEFAULT 0,
			rl_model_bucket TEXT NOT NULL DEFAULT '',
			rl_model_utilization REAL NOT NULL DEFAULT 0,
			rl_model_reset INTEGER NOT NULL DEFAULT 0,
			rl_representative_claim TEXT NOT NULL DEFAULT '',

			status_code INTEGER NOT NULL DEFAULT 0,
			stop_reason TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,

			source_file TEXT NOT NULL DEFAULT '',
			source_hash TEXT NOT NULL DEFAULT '',
			indexed_at INTEGER NOT NULL DEFAULT 0,

			it2_session_id TEXT NOT NULL DEFAULT '',
			client_pid INTEGER NOT NULL DEFAULT 0,

			user_hash TEXT NOT NULL DEFAULT '',
			account_uuid TEXT NOT NULL DEFAULT '',
			org_id TEXT NOT NULL DEFAULT '',

			context_breakdown_json TEXT NOT NULL DEFAULT '{}'
		);

		CREATE INDEX IF NOT EXISTS idx_apireq_session ON api_requests(session_id);
		CREATE INDEX IF NOT EXISTS idx_apireq_timestamp ON api_requests(timestamp);
		CREATE INDEX IF NOT EXISTS idx_apireq_model ON api_requests(model_family);
		CREATE INDEX IF NOT EXISTS idx_apireq_source ON api_requests(source_hash);

		CREATE TABLE IF NOT EXISTS rate_limit_snapshots (
			timestamp INTEGER NOT NULL,
			bucket TEXT NOT NULL,
			utilization REAL NOT NULL,
			reset_at INTEGER NOT NULL,
			PRIMARY KEY (timestamp, bucket)
		);

		CREATE INDEX IF NOT EXISTS idx_rl_bucket ON rate_limit_snapshots(bucket, timestamp);

		CREATE TABLE IF NOT EXISTS subagent_runs (
			parent_session_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			parent_claude_sid TEXT NOT NULL DEFAULT '',
			workspace TEXT NOT NULL DEFAULT '',
			git_common_dir TEXT NOT NULL DEFAULT '',
			agent_type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			enqueued_at INTEGER NOT NULL DEFAULT 0,
			dequeued_at INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT 0,
			ended_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT '',
			tool_use_id TEXT NOT NULL DEFAULT '',
			output_file TEXT NOT NULL DEFAULT '',
			worktree_path TEXT NOT NULL DEFAULT '',
			worktree_branch TEXT NOT NULL DEFAULT '',
			total_tokens INTEGER NOT NULL DEFAULT 0,
			tool_uses INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			entry_count INTEGER NOT NULL DEFAULT 0,
			source_path TEXT NOT NULL DEFAULT '',
			meta_path TEXT NOT NULL DEFAULT '',
			is_compaction INTEGER NOT NULL DEFAULT 0,
			indexed_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (parent_session_id, agent_id),
			FOREIGN KEY (parent_session_id) REFERENCES sessions(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_subagent_runs_started ON subagent_runs(started_at);
		CREATE INDEX IF NOT EXISTS idx_subagent_runs_workspace ON subagent_runs(workspace);
		CREATE INDEX IF NOT EXISTS idx_subagent_runs_git ON subagent_runs(git_common_dir);
		CREATE INDEX IF NOT EXISTS idx_subagent_runs_model ON subagent_runs(model);
		CREATE INDEX IF NOT EXISTS idx_subagent_runs_agent_type ON subagent_runs(agent_type);

	` + jobsAgentsSchema + `

		CREATE VIRTUAL TABLE IF NOT EXISTS session_fts USING fts5(
			title,
			content,
			agent,
			content=sessions,
			content_rowid=rowid,
			tokenize="porter unicode61"
		);

		CREATE TRIGGER IF NOT EXISTS sessions_ai AFTER INSERT ON sessions BEGIN
			INSERT INTO session_fts(rowid, title, content, agent)
			VALUES (new.rowid, new.title, new.content, new.agent);
		END;

		CREATE TRIGGER IF NOT EXISTS sessions_ad AFTER DELETE ON sessions BEGIN
			INSERT INTO session_fts(session_fts, rowid, title, content, agent)
			VALUES ('delete', old.rowid, old.title, old.content, old.agent);
		END;

		CREATE TRIGGER IF NOT EXISTS sessions_au AFTER UPDATE ON sessions BEGIN
			INSERT INTO session_fts(session_fts, rowid, title, content, agent)
			VALUES ('delete', old.rowid, old.title, old.content, old.agent);
			INSERT INTO session_fts(rowid, title, content, agent)
			VALUES (new.rowid, new.title, new.content, new.agent);
		END;
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	// Best-effort migrations for columns added after initial schema.
	for _, col := range []string{
		"ALTER TABLE sessions ADD COLUMN sparkline TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN team_name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN agent_name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN is_team_lead INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE session_links ADD COLUMN team_name TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE api_requests ADD COLUMN it2_session_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE api_requests ADD COLUMN client_pid INTEGER NOT NULL DEFAULT 0",
		"CREATE INDEX IF NOT EXISTS idx_apireq_it2 ON api_requests(it2_session_id)",
		"ALTER TABLE api_requests ADD COLUMN user_hash TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE api_requests ADD COLUMN account_uuid TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE api_requests ADD COLUMN org_id TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_apireq_account ON api_requests(account_uuid)",
		"ALTER TABLE api_requests ADD COLUMN context_breakdown_json TEXT NOT NULL DEFAULT '{}'",
		"ALTER TABLE sessions ADD COLUMN git_common_dir TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN branch TEXT NOT NULL DEFAULT ''",
		"CREATE INDEX IF NOT EXISTS idx_sessions_git_common_dir ON sessions(git_common_dir)",
		"ALTER TABLE sessions ADD COLUMN subagent_run_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN goal_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN active_goal_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN completed_goal_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN goals_json TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE sessions ADD COLUMN skill_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN selected_skill_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN loaded_skill_count INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE sessions ADD COLUMN skills_json TEXT NOT NULL DEFAULT '[]'",
	} {
		s.db.Exec(col) // ignore "duplicate column" errors
	}
	return nil
}

// BatchIndex adds or updates sessions atomically.
func (s *DB) BatchIndex(ctx context.Context, sessions []cass.Session) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	sessStmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO sessions (id, agent, title, workspace, source_path, started_at, ended_at, content, indexed_at,
			tool_calls, input_tokens, output_tokens, files_read, files_written, files_edited, lines_written,
			turns, duration_secs, subagent_spawns, it2_splits, it2_sends, it2_screens, it2_buffers,
			team_inbox_reads, team_inbox_sends, team_task_ops, team_spawns,
			goal_count, active_goal_count, completed_goal_count, goals_json,
			skill_count, selected_skill_count, loaded_skill_count, skills_json, sparkline, stats_json,
			team_name, agent_name, is_team_lead, git_common_dir, branch, subagent_run_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare sessions: %w", err)
	}
	defer sessStmt.Close()

	linkStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO session_links (session_id, source_session, target_session, kind, action, text, timestamp, team_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare links: %w", err)
	}
	defer linkStmt.Close()

	runStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO subagent_runs (
			parent_session_id, agent_id, parent_claude_sid, workspace, git_common_dir,
			agent_type, description, model,
			enqueued_at, dequeued_at, started_at, ended_at,
			status, tool_use_id, output_file, worktree_path, worktree_branch,
			total_tokens, tool_uses, duration_ms, entry_count,
			source_path, meta_path, is_compaction, indexed_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare subagent_runs: %w", err)
	}
	defer runStmt.Close()

	now := time.Now().Unix()
	for _, sess := range sessions {
		sess.Goals = normalizeGoals(sess.Goals)
		content := buildContent(sess)
		statsJSON, _ := json.Marshal(sess.Stats)
		goalsJSON, _ := json.Marshal(sess.Goals)
		skillsJSON, _ := json.Marshal(sess.Skills)
		goalCount, activeGoalCount, completedGoalCount := goalCounts(sess.Goals)
		skillCount, selectedSkillCount, loadedSkillCount := skillCounts(sess.Skills)
		_, err := sessStmt.ExecContext(ctx,
			sess.ID,
			sess.Agent,
			sess.Title,
			sess.Workspace,
			sess.SourcePath,
			sess.StartedAt.Unix(),
			sess.EndedAt.Unix(),
			content,
			now,
			sess.Stats.ToolCalls,
			sess.Stats.InputTokens,
			sess.Stats.OutputTokensSnapshot,
			sess.Stats.FilesRead,
			sess.Stats.FilesWritten,
			sess.Stats.FilesEdited,
			sess.Stats.LinesWritten,
			sess.Stats.Turns,
			sess.Stats.DurationSecs,
			sess.Stats.SubagentSpawns,
			sess.Stats.IT2Splits,
			sess.Stats.IT2Sends,
			sess.Stats.IT2Screens,
			sess.Stats.IT2Buffers,
			sess.Stats.TeamInboxReads,
			sess.Stats.TeamInboxSends,
			sess.Stats.TeamTaskOps,
			sess.Stats.TeamSpawns,
			goalCount,
			activeGoalCount,
			completedGoalCount,
			string(goalsJSON),
			skillCount,
			selectedSkillCount,
			loadedSkillCount,
			string(skillsJSON),
			sess.Stats.Sparkline,
			string(statsJSON),
			sess.TeamName,
			sess.AgentName,
			sess.IsTeamLead,
			sess.GitCommonDir,
			sess.Branch,
			len(sess.Subagents),
		)
		if err != nil {
			return fmt.Errorf("insert %s: %w", sess.ID, err)
		}

		// Subagent runs: clear and reinsert for this session.
		if _, err := tx.ExecContext(ctx, "DELETE FROM subagent_runs WHERE parent_session_id = ?", sess.ID); err != nil {
			return fmt.Errorf("clear subagent_runs %s: %w", sess.ID, err)
		}
		for _, run := range sess.Subagents {
			isCompaction := 0
			if run.IsCompaction {
				isCompaction = 1
			}
			if _, err := runStmt.ExecContext(ctx,
				sess.ID,
				run.AgentID,
				run.ParentClaudeSID,
				run.Workspace,
				run.GitCommonDir,
				run.AgentType,
				run.Description,
				run.Model,
				run.EnqueuedAt.Unix(),
				run.DequeuedAt.Unix(),
				run.StartedAt.Unix(),
				run.EndedAt.Unix(),
				run.Status,
				run.ToolUseID,
				run.OutputFile,
				run.WorktreePath,
				run.WorktreeBranch,
				run.TotalTokens,
				run.ToolUses,
				run.DurationMs,
				run.EntryCount,
				run.SourcePath,
				run.MetaPath,
				isCompaction,
				now,
			); err != nil {
				return fmt.Errorf("insert subagent_run %s/%s: %w", sess.ID, run.AgentID, err)
			}
		}

		// Store iTerm2 <-> Claude session mapping.
		if itermSID, ok := sess.Metadata["iterm_session"].(string); ok && itermSID != "" {
			claudeSID := ""
			if len(sess.Messages) > 0 {
				claudeSID = sess.Messages[0].ID
			}
			tx.ExecContext(ctx, `
				INSERT OR REPLACE INTO session_mapping (iterm_session, claude_session, cass_session, workspace, title, started_at)
				VALUES (?, ?, ?, ?, ?, ?)
			`, itermSID, claudeSID, sess.ID, sess.Workspace, sess.Title, sess.StartedAt.Unix())
		}

		// Store session links.
		if links, ok := sess.Metadata["session_links"].([]cass.SessionLink); ok {
			// Clear old links for this session.
			tx.ExecContext(ctx, "DELETE FROM session_links WHERE session_id = ?", sess.ID)
			for _, link := range links {
				linkStmt.ExecContext(ctx,
					sess.ID,
					link.SourceSession,
					link.TargetSession,
					link.Kind,
					link.Action,
					link.Text,
					link.Timestamp,
					link.TeamName,
				)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}
	// Checkpoint WAL after each batch to prevent unbounded WAL growth.
	s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	return nil
}

// Search executes a full-text query and returns matching results.
func (s *DB) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	var where []string
	var args []any

	// FTS5 match clause.
	if req.Query != "" {
		where = append(where, "session_fts MATCH ?")
		args = append(args, ftsQuery(req.Query))
	}
	if req.Filters.Agent != "" {
		where = append(where, agentFilter("s.agent"))
		args = append(args, agentFilterArgs(req.Filters.Agent)...)
	}
	if !req.Filters.After.IsZero() {
		where = append(where, "s.started_at >= ?")
		args = append(args, req.Filters.After.Unix())
	}
	if !req.Filters.Before.IsZero() {
		where = append(where, "s.started_at <= ?")
		args = append(args, req.Filters.Before.Unix())
	}
	if req.Filters.Workspace != "" {
		where = append(where, "s.workspace LIKE ?")
		args = append(args, "%"+req.Filters.Workspace+"%")
	}
	if req.Filters.GitCommonDir != "" {
		where = append(where, "s.git_common_dir = ?")
		args = append(args, req.Filters.GitCommonDir)
	}
	if req.Filters.Team != "" {
		where = append(where, "s.team_name = ?")
		args = append(args, req.Filters.Team)
	}
	if req.Filters.GoalStatus != "" {
		where = append(where, "s.goals_json LIKE ?")
		args = append(args, `%"effective_status":"`+req.Filters.GoalStatus+`"%`)
	}
	if req.Filters.Skill != "" {
		where = append(where, "s.skills_json LIKE ?")
		args = append(args, "%"+req.Filters.Skill+"%")
	}

	whereClause := ""
	if len(where) > 0 {
		whereClause = "WHERE " + strings.Join(where, " AND ")
	}

	// Resolve effective sort mode.
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
			orderClause = "ORDER BY score"
		} else {
			orderClause = "ORDER BY s.ended_at DESC"
		}
	case cass.SortStarted:
		orderClause = "ORDER BY s.started_at DESC"
	case cass.SortOldest:
		orderClause = "ORDER BY s.started_at ASC"
	default: // SortRecent
		orderClause = "ORDER BY s.ended_at DESC"
	}

	// Build query with BM25 ranking when doing FTS.
	statsCols := ", s." + strings.ReplaceAll(hitStatsCols, ", ", ", s.")
	var query string
	if req.Query != "" {
		query = fmt.Sprintf(`
			SELECT s.id, s.agent, s.title, snippet(session_fts, 1, '>>>', '<<<', '...', 40) as snip,
				bm25(session_fts, 5.0, 1.0, 2.0) as score, s.workspace, s.source_path, s.started_at%s
			FROM session_fts
			JOIN sessions s ON s.rowid = session_fts.rowid
			%s
			%s
			LIMIT ? OFFSET ?
		`, statsCols, whereClause, orderClause)
	} else {
		query = fmt.Sprintf(`
			SELECT s.id, s.agent, s.title, substr(s.content, 1, 200) as snip,
				0.0 as score, s.workspace, s.source_path, s.started_at%s
			FROM sessions s
			%s
			%s
			LIMIT ? OFFSET ?
		`, statsCols, whereClause, orderClause)
	}
	args = append(args, req.Limit, req.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var hits []cass.Hit
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	// Count total matches.
	var countQuery string
	countArgs := args[:len(args)-2] // strip LIMIT/OFFSET
	if req.Query != "" {
		countQuery = fmt.Sprintf(`
			SELECT count(*) FROM session_fts
			JOIN sessions s ON s.rowid = session_fts.rowid
			%s
		`, whereClause)
	} else {
		countQuery = fmt.Sprintf(`SELECT count(*) FROM sessions s %s`, whereClause)
	}

	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		total = len(hits)
	}

	return &cass.SearchResult{
		Hits:       hits,
		TotalCount: total,
	}, nil
}

// Delete removes sessions matching the filter.
func (s *DB) Delete(ctx context.Context, filter cass.DeleteFilter) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(filter.IDs))
		for i, id := range filter.IDs {
			args[i] = id
		}
		// Delete subagent_runs first; we don't rely on FK cascade because
		// SQLite's foreign_keys pragma is off by default per connection.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM subagent_runs WHERE parent_session_id IN (%s)", placeholders), args...); err != nil {
			return fmt.Errorf("delete subagent_runs by id: %w", err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM sessions WHERE id IN (%s)", placeholders), args...); err != nil {
			return fmt.Errorf("delete by id: %w", err)
		}
	}

	if filter.Agent != "" {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM subagent_runs WHERE parent_session_id IN (SELECT id FROM sessions WHERE "+agentFilter("agent")+")",
			agentFilterArgs(filter.Agent)...,
		); err != nil {
			return fmt.Errorf("delete subagent_runs by agent: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE "+agentFilter("agent"), agentFilterArgs(filter.Agent)...); err != nil {
			return fmt.Errorf("delete by agent: %w", err)
		}
	}

	return tx.Commit()
}

// agentFilter returns a SQL WHERE clause for matching agent names.
// Matches exactly or as a prefix so that e.g. "codex" finds both
// "codex-cli" and "codex-app".
func agentFilter(col string) string {
	return "(" + col + " = ? OR " + col + " LIKE ?)"
}

func agentFilterArgs(agent string) []any {
	return []any{agent, agent + "-%"}
}

// Close checkpoints the WAL and releases the database connection.
// Without checkpointing, the WAL file grows unboundedly and can reach
// several GB even when the main DB is small.
func (s *DB) Close() error {
	s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return s.db.Close()
}

// SetMeta stores a key-value pair in the metadata table.
func (s *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO metadata (key, value) VALUES (?, ?)`, key, value)
	return err
}

// Meta retrieves a metadata value by key.
func (s *DB) Meta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SourcePath returns the source file path for a session by its ID.
func (s *DB) SourcePath(ctx context.Context, id string) (string, error) {
	var path string
	err := s.db.QueryRowContext(ctx, `SELECT source_path FROM sessions WHERE id = ?`, id).Scan(&path)
	return path, err
}

// Session returns indexed metadata for a single session.
func (s *DB) Session(ctx context.Context, id string) (cass.Hit, error) {
	query := `SELECT id, agent, title, substr(content, 1, 200) as snip, 0.0 as score, workspace, source_path, started_at, ` + hitStatsCols + ` FROM sessions WHERE id = ?`
	return scanHit(s.db.QueryRowContext(ctx, query, id))
}

func scanHit(row hitScanner) (cass.Hit, error) {
	var h cass.Hit
	var startedUnix, endedUnix int64
	var statsJSON, goalsJSON, skillsJSON string
	var isTeamLead int
	if err := row.Scan(
		&h.SessionID, &h.Agent, &h.Title, &h.Snippet, &h.Score, &h.Workspace, &h.SourcePath, &startedUnix,
		&endedUnix, &h.ToolCalls, &h.Turns, &h.InputTokens, &h.OutputTokens, &h.FilesEdited, &h.LinesWritten, &h.DurationSecs,
		&h.Sparkline, &h.SubagentSpawns, &h.IT2Sends, &h.IT2Screens, &h.IT2Splits, &statsJSON, &h.TeamName, &h.AgentName, &isTeamLead,
		&h.GitCommonDir, &h.Branch, &goalsJSON, &h.GoalCount, &h.ActiveGoalCount, &h.CompletedGoalCount,
		&skillsJSON, &h.SkillCount, &h.SelectedSkillCount, &h.LoadedSkillCount,
	); err != nil {
		return cass.Hit{}, err
	}
	if startedUnix > 0 {
		h.StartedAt = time.Unix(startedUnix, 0).Format(time.RFC3339)
	}
	if endedUnix > 0 {
		h.EndedAt = time.Unix(endedUnix, 0).Format(time.RFC3339)
	}
	h.IsTeamLead = isTeamLead != 0
	if goalsJSON != "" {
		_ = json.Unmarshal([]byte(goalsJSON), &h.Goals)
		h.Goals = normalizeGoals(h.Goals)
	}
	if skillsJSON != "" {
		_ = json.Unmarshal([]byte(skillsJSON), &h.Skills)
	}
	if statsJSON != "" {
		var stats cass.SessionStats
		if json.Unmarshal([]byte(statsJSON), &stats) == nil {
			h.ToolBreakdown = stats.ToolBreakdown
			h.Compactions = stats.Compactions
			h.CacheReads = stats.CacheReads
			h.CacheCreationInputTokens = stats.CacheCreationInputTokens
			h.WorkflowCount = stats.WorkflowRuns
			h.WorkflowAgentCount = stats.WorkflowAgentRuns
			h.WorkflowTaskOpCount = stats.WorkflowTaskOps
		}
	}
	return h, nil
}

// SessionCount returns the number of indexed sessions.
func (s *DB) SessionCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&count)
	return count, err
}

// AggregateStats returns detailed aggregate statistics across all sessions,
// optionally filtered by time range.
func (s *DB) AggregateStats(ctx context.Context, after, before time.Time) (map[string]any, error) {
	where := "WHERE 1=1"
	var args []any
	if !after.IsZero() {
		where += " AND started_at >= ?"
		args = append(args, after.Unix())
	}
	if !before.IsZero() {
		where += " AND started_at <= ?"
		args = append(args, before.Unix())
	}

	row := s.db.QueryRowContext(ctx, `
		SELECT
			count(*) as session_count,
			coalesce(sum(tool_calls), 0),
			coalesce(sum(input_tokens), 0),
			coalesce(sum(output_tokens), 0),
			coalesce(sum(files_read), 0),
			coalesce(sum(files_written), 0),
			coalesce(sum(files_edited), 0),
			coalesce(sum(lines_written), 0),
			coalesce(sum(turns), 0),
			coalesce(sum(duration_secs), 0),
			coalesce(sum(subagent_spawns), 0),
			coalesce(sum(it2_splits), 0),
			coalesce(sum(it2_sends), 0),
			coalesce(sum(it2_screens), 0),
			coalesce(sum(it2_buffers), 0),
			coalesce(sum(team_inbox_reads), 0),
			coalesce(sum(team_inbox_sends), 0),
			coalesce(sum(team_task_ops), 0),
			coalesce(sum(team_spawns), 0),
			count(DISTINCT agent),
			count(DISTINCT workspace),
			coalesce(sum(goal_count), 0),
			coalesce(sum(active_goal_count), 0),
			coalesce(sum(completed_goal_count), 0),
			coalesce(sum(skill_count), 0),
			coalesce(sum(selected_skill_count), 0),
			coalesce(sum(loaded_skill_count), 0)
		FROM sessions `+where, args...)

	var (
		sessions, tools, inTok, outTok                   int
		fRead, fWritten, fEdited, lWritten               int
		turns, dur, subSpawns                            int
		it2Splits, it2Sends, it2Screens, it2Buffers      int
		teamInbox, teamSends, teamTasks, teamSpawns      int
		agents, workspaces                               int
		goalCount, activeGoalCount, completedGoalCount   int
		skillCount, selectedSkillCount, loadedSkillCount int
	)
	if err := row.Scan(
		&sessions, &tools, &inTok, &outTok,
		&fRead, &fWritten, &fEdited, &lWritten,
		&turns, &dur, &subSpawns,
		&it2Splits, &it2Sends, &it2Screens, &it2Buffers,
		&teamInbox, &teamSends, &teamTasks, &teamSpawns,
		&agents, &workspaces,
		&goalCount, &activeGoalCount, &completedGoalCount,
		&skillCount, &selectedSkillCount, &loadedSkillCount,
	); err != nil {
		return nil, fmt.Errorf("aggregate stats: %w", err)
	}

	// Top agents by session count.
	agentRows, err := s.db.QueryContext(ctx, `
		SELECT agent, count(*) as cnt FROM sessions `+where+`
		GROUP BY agent ORDER BY cnt DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer agentRows.Close()
	agentCounts := map[string]int{}
	for agentRows.Next() {
		var a string
		var c int
		agentRows.Scan(&a, &c)
		agentCounts[a] = c
	}

	// Top workspaces by session count.
	wsRows, err := s.db.QueryContext(ctx, `
		SELECT workspace, count(*) as cnt FROM sessions `+where+`
		GROUP BY workspace ORDER BY cnt DESC LIMIT 10`, args...)
	if err != nil {
		return nil, err
	}
	defer wsRows.Close()
	wsCounts := map[string]int{}
	for wsRows.Next() {
		var w string
		var c int
		wsRows.Scan(&w, &c)
		wsCounts[w] = c
	}

	// Top skills by session signal count.
	skillRows, err := s.db.QueryContext(ctx, `
		SELECT skills_json FROM sessions `+where+` AND skill_count > 0`, args...)
	if err != nil {
		return nil, err
	}
	defer skillRows.Close()
	topSkills := map[string]int{}
	var workflowRuns, workflowAgents, workflowTaskOps int
	for skillRows.Next() {
		var skillsJSON string
		if err := skillRows.Scan(&skillsJSON); err != nil {
			return nil, err
		}
		var skills []cass.SkillUse
		if json.Unmarshal([]byte(skillsJSON), &skills) != nil {
			continue
		}
		for _, sk := range skills {
			if sk.Name == "" {
				continue
			}
			n := sk.Count
			if n == 0 {
				n = 1
			}
			topSkills[sk.Name] += n
		}
	}

	statsRows, err := s.db.QueryContext(ctx, `SELECT stats_json FROM sessions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer statsRows.Close()
	for statsRows.Next() {
		var statsJSON string
		if err := statsRows.Scan(&statsJSON); err != nil {
			return nil, err
		}
		var stats cass.SessionStats
		if json.Unmarshal([]byte(statsJSON), &stats) != nil {
			continue
		}
		workflowRuns += stats.WorkflowRuns
		workflowAgents += stats.WorkflowAgentRuns
		workflowTaskOps += stats.WorkflowTaskOps
	}

	// Sessions per day (last 30 days).
	dailyRows, err := s.db.QueryContext(ctx, `
		SELECT date(started_at, 'unixepoch') as day, count(*) as cnt
		FROM sessions
		WHERE started_at > ?
		GROUP BY day ORDER BY day`,
		time.Now().AddDate(0, 0, -30).Unix())
	if err != nil {
		return nil, err
	}
	defer dailyRows.Close()
	daily := map[string]int{}
	for dailyRows.Next() {
		var d string
		var c int
		dailyRows.Scan(&d, &c)
		daily[d] = c
	}

	return map[string]any{
		"sessions":          sessions,
		"agents":            agents,
		"workspaces":        workspaces,
		"tool_calls":        tools,
		"input_tokens":      inTok,
		"output_tokens":     outTok,
		"total_tokens":      inTok + outTok,
		"files_read":        fRead,
		"files_written":     fWritten,
		"files_edited":      fEdited,
		"lines_written":     lWritten,
		"turns":             turns,
		"duration_secs":     dur,
		"subagent_spawns":   subSpawns,
		"it2_splits":        it2Splits,
		"it2_sends":         it2Sends,
		"it2_screens":       it2Screens,
		"it2_buffers":       it2Buffers,
		"team_inbox_reads":  teamInbox,
		"team_inbox_sends":  teamSends,
		"team_task_ops":     teamTasks,
		"team_spawns":       teamSpawns,
		"agents_breakdown":  agentCounts,
		"workspace_top":     wsCounts,
		"top_skills":        topSkills,
		"sessions_per_day":  daily,
		"goals":             goalCount,
		"active_goals":      activeGoalCount,
		"completed_goals":   completedGoalCount,
		"skills":            skillCount,
		"selected_skills":   selectedSkillCount,
		"loaded_skills":     loadedSkillCount,
		"workflows":         workflowRuns,
		"workflow_agents":   workflowAgents,
		"workflow_task_ops": workflowTaskOps,
	}, nil
}

// SaveMapping stores a mapping between iTerm2 session, Claude session, and CASS session IDs.
func (s *DB) SaveMapping(ctx context.Context, itermSID, claudeSID, cassSID, workspace, title string, startedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO session_mapping (iterm_session, claude_session, cass_session, workspace, title, started_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, itermSID, claudeSID, cassSID, workspace, title, startedAt)
	return err
}

// Mappings returns all session mappings, optionally filtered by iTerm2 or Claude session ID.
func (s *DB) Mappings(ctx context.Context, filter string) ([]SessionMapping, error) {
	query := `SELECT iterm_session, claude_session, cass_session, workspace, title, started_at FROM session_mapping`
	var args []any
	if filter != "" {
		query += " WHERE iterm_session LIKE ? OR claude_session LIKE ?"
		args = append(args, "%"+filter+"%", "%"+filter+"%")
	}
	query += " ORDER BY started_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mappings []SessionMapping
	for rows.Next() {
		var m SessionMapping
		if err := rows.Scan(&m.ItermSession, &m.ClaudeSession, &m.CASSSession, &m.Workspace, &m.Title, &m.StartedAt); err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}
	return mappings, rows.Err()
}

// SessionMapping maps between iTerm2, Claude, and CASS session identifiers.
type SessionMapping struct {
	ItermSession  string `json:"iterm_session"`
	ClaudeSession string `json:"claude_session"`
	CASSSession   string `json:"cass_session"`
	Workspace     string `json:"workspace"`
	Title         string `json:"title"`
	StartedAt     int64  `json:"started_at"`
}

// Links returns all session links, optionally filtered by session ID.
func (s *DB) Links(ctx context.Context, sessionID string) ([]cass.SessionLink, error) {
	query := `SELECT source_session, target_session, kind, action, text, timestamp, team_name FROM session_links`
	var args []any
	if sessionID != "" {
		query += " WHERE session_id = ?"
		args = append(args, sessionID)
	}
	query += " ORDER BY timestamp"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query links: %w", err)
	}
	defer rows.Close()

	var links []cass.SessionLink
	for rows.Next() {
		var l cass.SessionLink
		if err := rows.Scan(&l.SourceSession, &l.TargetSession, &l.Kind, &l.Action, &l.Text, &l.Timestamp, &l.TeamName); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// Goals returns goal-mode objectives joined with parent session metadata.
func (s *DB) Goals(ctx context.Context, status string, limit int) ([]cass.GoalHit, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, agent, title, workspace, source_path, started_at, ended_at, goals_json FROM sessions WHERE goal_count > 0`
	var args []any
	if status != "" {
		query += ` AND goals_json LIKE ?`
		args = append(args, `%"effective_status":"`+status+`"%`)
	}
	query += ` ORDER BY ended_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query goals: %w", err)
	}
	defer rows.Close()

	var hits []cass.GoalHit
	for rows.Next() {
		var sessionID, agent, title, workspace, sourcePath, goalsJSON string
		var startedUnix, endedUnix int64
		if err := rows.Scan(&sessionID, &agent, &title, &workspace, &sourcePath, &startedUnix, &endedUnix, &goalsJSON); err != nil {
			return nil, fmt.Errorf("scan goal session: %w", err)
		}
		var goals []cass.Goal
		if err := json.Unmarshal([]byte(goalsJSON), &goals); err != nil {
			continue
		}
		started, ended := "", ""
		if startedUnix > 0 {
			started = time.Unix(startedUnix, 0).Format(time.RFC3339)
		}
		if endedUnix > 0 {
			ended = time.Unix(endedUnix, 0).Format(time.RFC3339)
		}
		for _, g := range goals {
			g = cass.NormalizeGoal(g)
			if status != "" && g.EffectiveStatus != status {
				continue
			}
			hits = append(hits, cass.GoalHit{
				Goal:       g,
				SessionID:  sessionID,
				Agent:      agent,
				Title:      title,
				Workspace:  workspace,
				SourcePath: sourcePath,
				StartedAt:  started,
				EndedAt:    ended,
			})
		}
	}
	return hits, rows.Err()
}

// Skills returns skill usage records joined with parent session metadata.
func (s *DB) Skills(ctx context.Context, skill string, kind string, limit int) ([]cass.SkillHit, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, agent, title, workspace, source_path, started_at, ended_at, skills_json FROM sessions WHERE skill_count > 0`
	var args []any
	if skill != "" {
		query += ` AND skills_json LIKE ?`
		args = append(args, "%"+skill+"%")
	}
	if kind != "" {
		query += ` AND skills_json LIKE ?`
		args = append(args, `%"kind":"`+kind+`"%`)
	}
	query += ` ORDER BY ended_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query skills: %w", err)
	}
	defer rows.Close()

	var hits []cass.SkillHit
	for rows.Next() {
		var sessionID, agent, title, workspace, sourcePath, skillsJSON string
		var startedUnix, endedUnix int64
		if err := rows.Scan(&sessionID, &agent, &title, &workspace, &sourcePath, &startedUnix, &endedUnix, &skillsJSON); err != nil {
			return nil, fmt.Errorf("scan skill session: %w", err)
		}
		var skills []cass.SkillUse
		if err := json.Unmarshal([]byte(skillsJSON), &skills); err != nil {
			continue
		}
		started, ended := "", ""
		if startedUnix > 0 {
			started = time.Unix(startedUnix, 0).Format(time.RFC3339)
		}
		if endedUnix > 0 {
			ended = time.Unix(endedUnix, 0).Format(time.RFC3339)
		}
		for _, sk := range skills {
			if skill != "" && !strings.Contains(sk.Name, skill) && !strings.Contains(sk.Path, skill) {
				continue
			}
			if kind != "" && sk.Kind != kind {
				continue
			}
			hits = append(hits, cass.SkillHit{
				SkillUse:   sk,
				SessionID:  sessionID,
				Agent:      agent,
				Title:      title,
				Workspace:  workspace,
				SourcePath: sourcePath,
				StartedAt:  started,
				EndedAt:    ended,
			})
		}
	}
	return hits, rows.Err()
}

// SessionLabel returns a short label for a session identified by iTerm2 session ID prefix.
type SessionLabel struct {
	ItermSession string `json:"iterm_session"`
	Workspace    string `json:"workspace"`
	Title        string `json:"title"`
}

// ResolveLabels looks up human-readable labels for a set of iTerm2 session ID prefixes.
func (s *DB) ResolveLabels(ctx context.Context, prefixes []string) (map[string]SessionLabel, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}
	result := make(map[string]SessionLabel, len(prefixes))

	// Build a query that matches prefixes.
	var where []string
	var args []any
	for _, p := range prefixes {
		where = append(where, "iterm_session LIKE ?")
		args = append(args, p+"%")
	}
	query := fmt.Sprintf(`SELECT iterm_session, workspace, title FROM session_mapping WHERE %s`, strings.Join(where, " OR "))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var l SessionLabel
		if err := rows.Scan(&l.ItermSession, &l.Workspace, &l.Title); err != nil {
			return nil, err
		}
		// Store by the prefix that matched.
		for _, p := range prefixes {
			if strings.HasPrefix(l.ItermSession, p) {
				result[p] = l
				break
			}
		}
	}
	return result, rows.Err()
}

// GraphData returns combined links and node metadata for the session graph.
// If since is non-zero, only links after that time are included.
func (s *DB) GraphData(ctx context.Context, since time.Time) (*cass.GraphData, error) {
	// Get all links, optionally filtered by time.
	links, err := s.Links(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("graph links: %w", err)
	}

	// Filter by time.
	cutoff := ""
	if !since.IsZero() {
		cutoff = since.Format(time.RFC3339)
	}
	var filtered []cass.SessionLink
	var minTS, maxTS string
	for _, l := range links {
		if cutoff != "" && l.Timestamp != "" && l.Timestamp < cutoff {
			continue
		}
		filtered = append(filtered, l)
		if l.Timestamp != "" {
			if minTS == "" || l.Timestamp < minTS {
				minTS = l.Timestamp
			}
			if maxTS == "" || l.Timestamp > maxTS {
				maxTS = l.Timestamp
			}
		}
	}

	// Collect unique session IDs (iTerm2 short prefixes) from links.
	seen := map[string]bool{}
	for _, l := range filtered {
		if l.SourceSession != "" {
			seen[l.SourceSession] = true
		}
		if l.TargetSession != "" {
			seen[l.TargetSession] = true
		}
	}

	var prefixes []string
	for id := range seen {
		prefixes = append(prefixes, id)
	}

	// Resolve labels from session_mapping.
	labels, err := s.ResolveLabels(ctx, prefixes)
	if err != nil {
		return nil, fmt.Errorf("graph labels: %w", err)
	}

	// Look up session stats for nodes that have a cass_session mapping.
	type nodeMeta struct {
		ToolCalls int
		Turns     int
		Tokens    int
		IsActive  bool
		TeamName  string
		AgentName string
	}
	nodeStats := map[string]nodeMeta{}
	for id, label := range labels {
		// Query from sessions table via the mapping's cass_session.
		var toolCalls, turns, inputTokens, outputTokens, endedAt int
		var teamName, agentName string
		row := s.db.QueryRowContext(ctx,
			`SELECT s.tool_calls, s.turns, s.input_tokens, s.output_tokens, s.ended_at, s.team_name, s.agent_name
			 FROM session_mapping m
			 JOIN sessions s ON s.id = m.cass_session
			 WHERE m.iterm_session LIKE ?
			 LIMIT 1`,
			label.ItermSession+"%")
		if err := row.Scan(&toolCalls, &turns, &inputTokens, &outputTokens, &endedAt, &teamName, &agentName); err == nil {
			isActive := time.Since(time.Unix(int64(endedAt), 0)) < time.Hour
			nodeStats[id] = nodeMeta{toolCalls, turns, inputTokens + outputTokens, isActive, teamName, agentName}
		}
	}

	// Build a map from agent-name node IDs to session workspace/title via team_configs.
	// Team link nodes have IDs like "researcher@work-team" or "team-lead".
	// team_configs.lead_session_id is the authoritative Claude session UUID for team leads.
	type teamNodeInfo struct {
		Workspace string
		Title     string
		TeamName  string
	}
	teamNodeMeta := map[string]teamNodeInfo{}
	for _, id := range prefixes {
		// Team agent nodes contain '@' (e.g. "researcher@work-team") or are plain agent names.
		// Look them up by joining team_configs with sessions on lead_session_id.
		var workspace, title, teamName string
		row := s.db.QueryRowContext(ctx, `
			SELECT s.workspace, s.title, tc.name
			FROM team_configs tc
			JOIN sessions s ON s.id = tc.lead_session_id
			WHERE tc.lead_agent_id = ? OR (tc.lead_agent_id LIKE ? AND tc.lead_agent_id != '')
			LIMIT 1`, id, id+"@%")
		if err := row.Scan(&workspace, &title, &teamName); err == nil {
			teamNodeMeta[id] = teamNodeInfo{workspace, title, teamName}
		}
	}

	// Build nodes.
	var nodes []cass.GraphNode
	for _, id := range prefixes {
		node := cass.GraphNode{ID: id}
		if label, ok := labels[id]; ok {
			node.Workspace = label.Workspace
			node.Title = label.Title
		}
		if m, ok := nodeStats[id]; ok {
			node.ToolCalls = m.ToolCalls
			node.Turns = m.Turns
			node.Tokens = m.Tokens
			node.IsActive = m.IsActive
			node.TeamName = m.TeamName
			node.AgentName = m.AgentName
		}
		// Enrich team agent nodes with workspace/title from team_configs if not already set.
		if node.Workspace == "" {
			if tm, ok := teamNodeMeta[id]; ok {
				node.Workspace = tm.Workspace
				node.Title = tm.Title
				if node.TeamName == "" {
					node.TeamName = tm.TeamName
				}
			}
		}
		nodes = append(nodes, node)
	}

	return &cass.GraphData{
		Nodes: nodes,
		Links: filtered,
		TimeRange: cass.TimeRange{
			Min: minTS,
			Max: maxTS,
		},
	}, nil
}

// BatchIndexRequests adds or updates API request records atomically.
func (s *DB) BatchIndexRequests(ctx context.Context, requests []cass.APIRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO api_requests (
			id, session_id, request_id, timestamp,
			model, model_family, purpose,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			system_prompt_bytes, tool_definition_bytes, conversation_bytes, total_request_bytes,
			rl_5h_utilization, rl_5h_reset, rl_7d_utilization, rl_7d_reset,
			rl_model_bucket, rl_model_utilization, rl_model_reset, rl_representative_claim,
			status_code, stop_reason, duration_ms,
			source_file, source_hash, indexed_at,
			it2_session_id, client_pid,
			user_hash, account_uuid, org_id,
			context_breakdown_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare api_requests: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, r := range requests {
		breakdownJSON := "{}"
		if r.Breakdown != nil {
			if b, err := json.Marshal(r.Breakdown); err == nil {
				breakdownJSON = string(b)
			}
		}

		_, err := stmt.ExecContext(ctx,
			r.ID, r.SessionID, r.RequestID, r.Timestamp,
			r.Model, r.ModelFamily, r.Purpose,
			r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens,
			r.SystemPromptBytes, r.ToolDefinitionBytes, r.ConversationBytes, r.TotalRequestBytes,
			r.RateLimits.Utilization5h, r.RateLimits.Reset5h,
			r.RateLimits.Utilization7d, r.RateLimits.Reset7d,
			r.RateLimits.ModelBucket, r.RateLimits.ModelUtilization, r.RateLimits.ModelReset,
			r.RateLimits.RepresentativeClaim,
			r.StatusCode, r.StopReason, r.DurationMs,
			r.SourceFile, r.SourceHash, now,
			r.IT2SessionID, r.ClientPID,
			r.UserHash, r.AccountUUID, r.OrgID,
			breakdownJSON,
		)
		if err != nil {
			return fmt.Errorf("insert request %s: %w", r.ID, err)
		}
	}

	return tx.Commit()
}

// SaveRateLimitSnapshots stores rate-limit utilization data points.
func (s *DB) SaveRateLimitSnapshots(ctx context.Context, snapshots []cass.RateLimitSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO rate_limit_snapshots (timestamp, bucket, utilization, reset_at)
		VALUES (?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("prepare rl snapshots: %w", err)
	}
	defer stmt.Close()

	for _, s := range snapshots {
		// Store global 5h bucket.
		if s.Utilization5h > 0 || s.Reset5h > 0 {
			stmt.ExecContext(ctx, s.Timestamp, "5h", s.Utilization5h, s.Reset5h)
		}
		// Store global 7d bucket.
		if s.Utilization7d > 0 || s.Reset7d > 0 {
			stmt.ExecContext(ctx, s.Timestamp, "7d", s.Utilization7d, s.Reset7d)
		}
		// Store per-model sub-bucket.
		if s.ModelBucket != "" {
			stmt.ExecContext(ctx, s.Timestamp, s.ModelBucket, s.ModelUtilization, s.ModelReset)
		}
	}

	return tx.Commit()
}

// QueryRequests returns API requests for a session, ordered by timestamp.
func (s *DB) QueryRequests(ctx context.Context, sessionID string) ([]cass.APIRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, request_id, timestamp,
			model, model_family, purpose,
			input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
			system_prompt_bytes, tool_definition_bytes, conversation_bytes, total_request_bytes,
			rl_5h_utilization, rl_5h_reset, rl_7d_utilization, rl_7d_reset,
			rl_model_bucket, rl_model_utilization, rl_model_reset, rl_representative_claim,
			status_code, stop_reason, duration_ms,
			source_file, source_hash,
			it2_session_id, client_pid,
			user_hash, account_uuid, org_id,
			context_breakdown_json
		FROM api_requests
		WHERE session_id = ? OR (session_id = '' AND it2_session_id = ?)
		ORDER BY timestamp
	`, sessionID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("query requests: %w", err)
	}
	defer rows.Close()

	var results []cass.APIRequest
	for rows.Next() {
		var r cass.APIRequest
		var breakdownJSON string
		if err := rows.Scan(
			&r.ID, &r.SessionID, &r.RequestID, &r.Timestamp,
			&r.Model, &r.ModelFamily, &r.Purpose,
			&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens,
			&r.SystemPromptBytes, &r.ToolDefinitionBytes, &r.ConversationBytes, &r.TotalRequestBytes,
			&r.RateLimits.Utilization5h, &r.RateLimits.Reset5h,
			&r.RateLimits.Utilization7d, &r.RateLimits.Reset7d,
			&r.RateLimits.ModelBucket, &r.RateLimits.ModelUtilization, &r.RateLimits.ModelReset,
			&r.RateLimits.RepresentativeClaim,
			&r.StatusCode, &r.StopReason, &r.DurationMs,
			&r.SourceFile, &r.SourceHash,
			&r.IT2SessionID, &r.ClientPID,
			&r.UserHash, &r.AccountUUID, &r.OrgID,
			&breakdownJSON,
		); err != nil {
			return nil, fmt.Errorf("scan request: %w", err)
		}
		if breakdownJSON != "" && breakdownJSON != "{}" {
			var bd cass.ContextBreakdown
			if err := json.Unmarshal([]byte(breakdownJSON), &bd); err == nil {
				r.Breakdown = &bd
			}
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// RateLimitTrend returns rate-limit utilization over time for a given bucket.
func (s *DB) RateLimitTrend(ctx context.Context, bucket string, since time.Time) ([]cass.RateLimitSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT timestamp, utilization, reset_at
		FROM rate_limit_snapshots
		WHERE bucket = ? AND timestamp >= ?
		ORDER BY timestamp
	`, bucket, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("rl trend: %w", err)
	}
	defer rows.Close()

	var results []cass.RateLimitSnapshot
	for rows.Next() {
		var ts, resetAt int64
		var util float64
		if err := rows.Scan(&ts, &util, &resetAt); err != nil {
			return nil, fmt.Errorf("scan rl: %w", err)
		}
		snap := cass.RateLimitSnapshot{Timestamp: ts}
		switch bucket {
		case "5h":
			snap.Utilization5h = util
			snap.Reset5h = resetAt
		case "7d":
			snap.Utilization7d = util
			snap.Reset7d = resetAt
		default:
			snap.ModelBucket = bucket
			snap.ModelUtilization = util
			snap.ModelReset = resetAt
		}
		results = append(results, snap)
	}
	return results, rows.Err()
}

// APIRequestCount returns the number of indexed API requests.
func (s *DB) APIRequestCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM api_requests`).Scan(&count)
	return count, err
}

// DailyTokenRow holds per-day aggregate token counts from api_requests.
type DailyTokenRow struct {
	Day                 string `json:"day"`
	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	SystemPromptBytes   int    `json:"system_prompt_bytes"`
	ToolDefinitionBytes int    `json:"tool_definition_bytes"`
	ConversationBytes   int    `json:"conversation_bytes"`
	Requests            int    `json:"requests"`
}

// DailyTokenUsage returns per-day token totals from api_requests for the given window.
// after zero means no lower bound.
func (s *DB) DailyTokenUsage(ctx context.Context, after time.Time) ([]DailyTokenRow, error) {
	where := "WHERE timestamp > 0"
	var args []any
	if !after.IsZero() {
		where += " AND timestamp >= ?"
		args = append(args, after.Unix())
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			date(timestamp, 'unixepoch') as day,
			coalesce(sum(input_tokens), 0),
			coalesce(sum(output_tokens), 0),
			coalesce(sum(cache_read_tokens), 0),
			coalesce(sum(cache_creation_tokens), 0),
			coalesce(sum(system_prompt_bytes), 0),
			coalesce(sum(tool_definition_bytes), 0),
			coalesce(sum(conversation_bytes), 0),
			count(*) as requests
		FROM api_requests `+where+`
		GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return nil, fmt.Errorf("daily token usage: %w", err)
	}
	defer rows.Close()

	var result []DailyTokenRow
	for rows.Next() {
		var r DailyTokenRow
		if err := rows.Scan(
			&r.Day, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens,
			&r.SystemPromptBytes, &r.ToolDefinitionBytes, &r.ConversationBytes, &r.Requests,
		); err != nil {
			return nil, fmt.Errorf("scan daily tokens: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// TeamConfig holds the parsed config.json for a Claude Code agent team.
type TeamConfig struct {
	Name          string `json:"name"`
	LeadSessionID string `json:"lead_session_id"` // authoritative FK to Claude session UUID.
	LeadAgentID   string `json:"lead_agent_id"`
	Description   string `json:"description"`
	CreatedAt     int64  `json:"created_at"`
	MembersJSON   string `json:"members_json"` // raw JSON array of member objects.
}

// SaveTeamConfig upserts a team config record.
func (s *DB) SaveTeamConfig(ctx context.Context, tc TeamConfig) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO team_configs
			(name, lead_session_id, lead_agent_id, description, created_at, members_json, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tc.Name, tc.LeadSessionID, tc.LeadAgentID, tc.Description, tc.CreatedAt,
		tc.MembersJSON, time.Now().Unix(),
	)
	return err
}

// TeamConfigs returns all indexed team configurations.
func (s *DB) TeamConfigs(ctx context.Context) ([]TeamConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, lead_session_id, lead_agent_id, description, created_at, members_json
		FROM team_configs ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("team configs: %w", err)
	}
	defer rows.Close()
	var result []TeamConfig
	for rows.Next() {
		var tc TeamConfig
		if err := rows.Scan(&tc.Name, &tc.LeadSessionID, &tc.LeadAgentID, &tc.Description, &tc.CreatedAt, &tc.MembersJSON); err != nil {
			return nil, fmt.Errorf("scan team config: %w", err)
		}
		result = append(result, tc)
	}
	return result, rows.Err()
}

// buildContent concatenates all message content for full-text indexing.
func buildContent(sess cass.Session) string {
	var b strings.Builder
	for _, goal := range sess.Goals {
		if goal.Objective == "" {
			continue
		}
		b.WriteString("goal ")
		if status := cass.GoalEffectiveStatus(goal); status != "" {
			b.WriteString(status)
			b.WriteByte(' ')
		}
		b.WriteString(goal.Objective)
		b.WriteByte('\n')
		for _, gate := range goal.CompletionGates {
			if gate.Name == "" {
				continue
			}
			b.WriteString("goal gate ")
			if gate.Status != "" {
				b.WriteString(gate.Status)
				b.WriteByte(' ')
			}
			b.WriteString(gate.Name)
			b.WriteByte('\n')
		}
	}
	for _, skill := range sess.Skills {
		if skill.Name == "" {
			continue
		}
		b.WriteString("skill ")
		if skill.Kind != "" {
			b.WriteString(skill.Kind)
			b.WriteByte(' ')
		}
		b.WriteString(skill.Name)
		if skill.Path != "" {
			b.WriteByte(' ')
			b.WriteString(skill.Path)
		}
		b.WriteByte('\n')
	}
	for _, msg := range sess.Messages {
		if msg.Content == "" {
			continue
		}
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

func goalCounts(goals []cass.Goal) (total, active, completed int) {
	total = len(goals)
	for _, g := range goals {
		switch cass.GoalEffectiveStatus(g) {
		case "complete", "completed":
			completed++
		case "active", "blocked":
			active++
		}
	}
	return total, active, completed
}

func normalizeGoals(goals []cass.Goal) []cass.Goal {
	if len(goals) == 0 {
		return goals
	}
	out := make([]cass.Goal, len(goals))
	for i, goal := range goals {
		out[i] = cass.NormalizeGoal(goal)
	}
	return out
}

func skillCounts(skills []cass.SkillUse) (total, selected, loaded int) {
	total = len(skills)
	for _, s := range skills {
		switch s.Kind {
		case "selected":
			selected++
		case "loaded", "expanded":
			loaded++
		}
	}
	return total, selected, loaded
}
