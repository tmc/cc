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

// sqliteBackend implements Backend using SQLite FTS5.
// It is a thin wrapper around *sql.DB so the same migrate/query logic
// used by Store can be reused for both the default and porter variants.
type sqliteBackend struct {
	db          *sql.DB
	tokenizer   string // "unicode61" or "porter unicode61"
	maxFTSBytes int
}

func openSQLite(cfg BackendConfig) (*sqliteBackend, error) {
	return openSQLiteWith(cfg, "unicode61")
}

func openSQLitePorter(cfg BackendConfig) (*sqliteBackend, error) {
	return openSQLiteWith(cfg, "porter unicode61")
}

func openSQLiteWith(cfg BackendConfig, tokenizer string) (*sqliteBackend, error) {
	path := cfg.Path
	if path == "" {
		path = ":memory:"
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(30 * time.Second)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}

	b := &sqliteBackend{db: db, tokenizer: tokenizer, maxFTSBytes: cfg.MaxFTSBytes}
	// maxFTSBytes == 0 means no cap; buildContentCapped handles that correctly.
	if err := b.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return b, nil
}

func (b *sqliteBackend) migrate() error {
	// The FTS table definition varies by tokenizer, so we construct it
	// dynamically. The rest of the schema is identical across SQLite variants.
	ftsCreate := fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS session_fts USING fts5(
			title,
			content,
			agent,
			content=sessions,
			content_rowid=rowid,
			tokenize="%s"
		);`, b.tokenizer)

	schema := sessionsSchema + ftsCreate + triggersSchema
	if _, err := b.db.Exec(schema); err != nil {
		return err
	}
	for _, col := range []string{
		"ALTER TABLE sessions ADD COLUMN git_common_dir TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE sessions ADD COLUMN branch TEXT NOT NULL DEFAULT ''",
	} {
		b.db.Exec(col)
	}
	return nil
}

func (b *sqliteBackend) BatchIndex(ctx context.Context, sessions []cass.Session) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, insertSessionSQL)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, sess := range sessions {
		content := buildContentCapped(sess, b.maxFTSBytes)
		statsJSON, _ := json.Marshal(sess.Stats)
		if _, err := stmt.ExecContext(ctx,
			sess.ID, sess.Agent, sess.Title, sess.Workspace, sess.SourcePath,
			sess.StartedAt.Unix(), sess.EndedAt.Unix(), content, now,
			sess.Stats.ToolCalls, sess.Stats.InputTokens, sess.Stats.OutputTokens,
			sess.Stats.FilesRead, sess.Stats.FilesWritten, sess.Stats.FilesEdited,
			sess.Stats.LinesWritten, sess.Stats.Turns, sess.Stats.DurationSecs,
			sess.Stats.SubagentSpawns, sess.Stats.IT2Splits, sess.Stats.IT2Sends,
			sess.Stats.IT2Screens, sess.Stats.IT2Buffers,
			sess.Stats.TeamInboxReads, sess.Stats.TeamInboxSends,
			sess.Stats.TeamTaskOps, sess.Stats.TeamSpawns,
			sess.Stats.Sparkline, string(statsJSON),
			sess.TeamName, sess.AgentName, sess.IsTeamLead,
			sess.GitCommonDir, sess.Branch,
		); err != nil {
			return fmt.Errorf("insert %s: %w", sess.ID, err)
		}
	}
	return tx.Commit()
}

func (b *sqliteBackend) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	var where []string
	var args []any

	if req.Query != "" {
		where = append(where, "session_fts MATCH ?")
		args = append(args, req.Query)
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
			orderClause = "ORDER BY score"
		} else {
			orderClause = "ORDER BY s.ended_at DESC"
		}
	case cass.SortStarted:
		orderClause = "ORDER BY s.started_at DESC"
	case cass.SortOldest:
		orderClause = "ORDER BY s.started_at ASC"
	default:
		orderClause = "ORDER BY s.ended_at DESC"
	}

	statsCols := `, s.ended_at, s.tool_calls, s.turns, s.input_tokens, s.output_tokens, s.files_edited, s.lines_written, s.duration_secs, s.sparkline, s.it2_sends, s.it2_screens, s.it2_splits, s.stats_json, s.team_name, s.agent_name, s.is_team_lead, s.git_common_dir, s.branch`
	var query string
	if req.Query != "" {
		query = fmt.Sprintf(`
			SELECT s.id, s.agent, s.title, snippet(session_fts, 1, '>>>', '<<<', '...', 40) as snip,
				bm25(session_fts, 5.0, 1.0, 2.0) as score, s.workspace, s.source_path, s.started_at%s
			FROM session_fts
			JOIN sessions s ON s.rowid = session_fts.rowid
			%s %s LIMIT ? OFFSET ?`, statsCols, whereClause, orderClause)
	} else {
		query = fmt.Sprintf(`
			SELECT s.id, s.agent, s.title, substr(s.content, 1, 200) as snip,
				0.0 as score, s.workspace, s.source_path, s.started_at%s
			FROM sessions s
			%s %s LIMIT ? OFFSET ?`, statsCols, whereClause, orderClause)
	}
	args = append(args, req.Limit, req.Offset)

	rows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var hits []cass.Hit
	for rows.Next() {
		var h cass.Hit
		var startedUnix, endedUnix int64
		var statsJSON string
		var isTeamLead int
		if err := rows.Scan(
			&h.SessionID, &h.Agent, &h.Title, &h.Snippet, &h.Score,
			&h.Workspace, &h.SourcePath, &startedUnix,
			&endedUnix, &h.ToolCalls, &h.Turns, &h.InputTokens, &h.OutputTokens,
			&h.FilesEdited, &h.LinesWritten, &h.DurationSecs,
			&h.Sparkline, &h.IT2Sends, &h.IT2Screens, &h.IT2Splits,
			&statsJSON, &h.TeamName, &h.AgentName, &isTeamLead,
			&h.GitCommonDir, &h.Branch,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if startedUnix > 0 {
			h.StartedAt = time.Unix(startedUnix, 0).Format(time.RFC3339)
		}
		if endedUnix > 0 {
			h.EndedAt = time.Unix(endedUnix, 0).Format(time.RFC3339)
		}
		h.IsTeamLead = isTeamLead != 0
		if statsJSON != "" {
			var stats cass.SessionStats
			if json.Unmarshal([]byte(statsJSON), &stats) == nil {
				h.ToolBreakdown = stats.ToolBreakdown
				h.Compactions = stats.Compactions
				h.CacheReads = stats.CacheReads
				h.CacheCreationInputTokens = stats.CacheCreationInputTokens
			}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	countArgs := args[:len(args)-2]
	var countQuery string
	if req.Query != "" {
		countQuery = fmt.Sprintf(`SELECT count(*) FROM session_fts JOIN sessions s ON s.rowid = session_fts.rowid %s`, whereClause)
	} else {
		countQuery = fmt.Sprintf(`SELECT count(*) FROM sessions s %s`, whereClause)
	}
	var total int
	if err := b.db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		total = len(hits)
	}

	return &cass.SearchResult{Hits: hits, TotalCount: total}, nil
}

func (b *sqliteBackend) SessionCount(ctx context.Context) (int, error) {
	var n int
	err := b.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&n)
	return n, err
}

func (b *sqliteBackend) Close() error { return b.db.Close() }

// BackendStats implements Statter.
func (b *sqliteBackend) BackendStats(ctx context.Context) (Stats, error) {
	var s Stats
	b.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&s.TotalRows)

	// Sum page usage of FTS shadow tables.
	rows, err := b.db.QueryContext(ctx, `
		SELECT name, pgsize*pageno AS bytes
		FROM dbstat
		WHERE name LIKE 'session_fts%'`)
	if err == nil {
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var name string
			var bytes int64
			rows.Scan(&name, &bytes)
			if !seen[name] {
				seen[name] = true
				s.IndexSizeBytes += bytes
			}
		}
	}
	return s, nil
}

// buildContentCapped concatenates message content for FTS indexing.
// When maxBytes is 0 the full content is indexed (no cap).
func buildContentCapped(sess cass.Session, maxBytes int) string {
	var b strings.Builder
	for _, msg := range sess.Messages {
		if msg.Content == "" {
			continue
		}
		if maxBytes > 0 {
			remaining := maxBytes - b.Len()
			if remaining <= 0 {
				break
			}
			if len(msg.Content) > remaining {
				b.WriteString(msg.Content[:remaining])
				break
			}
		}
		b.WriteString(msg.Content)
		b.WriteByte('\n')
	}
	return b.String()
}

// sessionsSchema is the DDL for the main sessions table (shared across SQLite variants).
const sessionsSchema = `
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
		sparkline TEXT NOT NULL DEFAULT '',
		stats_json TEXT NOT NULL DEFAULT '{}',
		team_name TEXT NOT NULL DEFAULT '',
		agent_name TEXT NOT NULL DEFAULT '',
		is_team_lead INTEGER NOT NULL DEFAULT 0,
		git_common_dir TEXT NOT NULL DEFAULT '',
		branch TEXT NOT NULL DEFAULT ''
	);
`

// triggersSchema keeps session_fts in sync with sessions.
const triggersSchema = `
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

// insertSessionSQL is the upsert used by BatchIndex.
const insertSessionSQL = `
	INSERT OR REPLACE INTO sessions (
		id, agent, title, workspace, source_path, started_at, ended_at, content, indexed_at,
		tool_calls, input_tokens, output_tokens, files_read, files_written, files_edited,
		lines_written, turns, duration_secs, subagent_spawns, it2_splits, it2_sends,
		it2_screens, it2_buffers, team_inbox_reads, team_inbox_sends, team_task_ops,
		team_spawns, sparkline, stats_json, team_name, agent_name, is_team_lead,
		git_common_dir, branch
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`
