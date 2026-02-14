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

// Store implements cass.Index using SQLite with FTS5.
type Store struct {
	db *sql.DB
}

// New opens or creates a SQLite store at the given path.
func New(dbPath string) (*Store, error) {
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
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
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

		CREATE VIRTUAL TABLE IF NOT EXISTS session_fts USING fts5(
			title,
			content,
			agent,
			content=sessions,
			content_rowid=rowid
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
	} {
		s.db.Exec(col) // ignore "duplicate column" errors
	}
	return nil
}

// BatchIndex adds or updates sessions atomically.
func (s *Store) BatchIndex(ctx context.Context, sessions []cass.Session) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	sessStmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO sessions (id, agent, title, workspace, source_path, started_at, ended_at, content, indexed_at,
			tool_calls, input_tokens, output_tokens, files_read, files_written, files_edited, lines_written,
			turns, duration_secs, subagent_spawns, it2_splits, it2_sends, it2_screens, it2_buffers,
			team_inbox_reads, team_inbox_sends, team_task_ops, team_spawns, sparkline, stats_json,
			team_name, agent_name, is_team_lead)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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

	now := time.Now().Unix()
	for _, sess := range sessions {
		content := buildContent(sess)
		statsJSON, _ := json.Marshal(sess.Stats)
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
			sess.Stats.OutputTokens,
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
			sess.Stats.Sparkline,
			string(statsJSON),
			sess.TeamName,
			sess.AgentName,
			sess.IsTeamLead,
		)
		if err != nil {
			return fmt.Errorf("insert %s: %w", sess.ID, err)
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

	return tx.Commit()
}

// Search executes a full-text query and returns matching results.
func (s *Store) Search(ctx context.Context, req cass.SearchRequest) (*cass.SearchResult, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	var where []string
	var args []any

	// FTS5 match clause.
	if req.Query != "" {
		where = append(where, "session_fts MATCH ?")
		args = append(args, req.Query)
	}
	if req.Filters.Agent != "" {
		where = append(where, "s.agent = ?")
		args = append(args, req.Filters.Agent)
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
	if req.Filters.Team != "" {
		where = append(where, "s.team_name = ?")
		args = append(args, req.Filters.Team)
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
	statsCols := `, s.ended_at, s.tool_calls, s.turns, s.input_tokens, s.output_tokens, s.files_edited, s.lines_written, s.duration_secs, s.sparkline, s.it2_sends, s.it2_screens, s.it2_splits, s.stats_json, s.team_name, s.agent_name, s.is_team_lead`
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
		var h cass.Hit
		var startedUnix, endedUnix int64
		var statsJSON string
		var isTeamLead int
		if err := rows.Scan(&h.SessionID, &h.Agent, &h.Title, &h.Snippet, &h.Score, &h.Workspace, &h.SourcePath, &startedUnix,
			&endedUnix, &h.ToolCalls, &h.Turns, &h.InputTokens, &h.OutputTokens, &h.FilesEdited, &h.LinesWritten, &h.DurationSecs,
			&h.Sparkline, &h.IT2Sends, &h.IT2Screens, &h.IT2Splits, &statsJSON, &h.TeamName, &h.AgentName, &isTeamLead); err != nil {
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
				if len(stats.ToolBreakdown) > 0 {
					h.ToolBreakdown = stats.ToolBreakdown
				}
				h.Compactions = stats.Compactions
			}
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
func (s *Store) Delete(ctx context.Context, filter cass.DeleteFilter) error {
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
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM sessions WHERE id IN (%s)", placeholders), args...); err != nil {
			return fmt.Errorf("delete by id: %w", err)
		}
	}

	if filter.Agent != "" {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE agent = ?", filter.Agent); err != nil {
			return fmt.Errorf("delete by agent: %w", err)
		}
	}

	return tx.Commit()
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// SetMeta stores a key-value pair in the metadata table.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO metadata (key, value) VALUES (?, ?)`, key, value)
	return err
}

// GetMeta retrieves a metadata value by key.
func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// SessionCount returns the number of indexed sessions.
func (s *Store) SessionCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sessions`).Scan(&count)
	return count, err
}

// AggregateStats returns detailed aggregate statistics across all sessions,
// optionally filtered by time range.
func (s *Store) AggregateStats(ctx context.Context, after, before time.Time) (map[string]any, error) {
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
			count(DISTINCT workspace)
		FROM sessions `+where, args...)

	var (
		sessions, tools, inTok, outTok              int
		fRead, fWritten, fEdited, lWritten          int
		turns, dur, subSpawns                       int
		it2Splits, it2Sends, it2Screens, it2Buffers int
		teamInbox, teamSends, teamTasks, teamSpawns int
		agents, workspaces                          int
	)
	if err := row.Scan(
		&sessions, &tools, &inTok, &outTok,
		&fRead, &fWritten, &fEdited, &lWritten,
		&turns, &dur, &subSpawns,
		&it2Splits, &it2Sends, &it2Screens, &it2Buffers,
		&teamInbox, &teamSends, &teamTasks, &teamSpawns,
		&agents, &workspaces,
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
		"sessions_per_day":  daily,
	}, nil
}

// SaveMapping stores a mapping between iTerm2 session, Claude session, and CASS session IDs.
func (s *Store) SaveMapping(ctx context.Context, itermSID, claudeSID, cassSID, workspace, title string, startedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO session_mapping (iterm_session, claude_session, cass_session, workspace, title, started_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, itermSID, claudeSID, cassSID, workspace, title, startedAt)
	return err
}

// Mappings returns all session mappings, optionally filtered by iTerm2 or Claude session ID.
func (s *Store) Mappings(ctx context.Context, filter string) ([]SessionMapping, error) {
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
func (s *Store) Links(ctx context.Context, sessionID string) ([]cass.SessionLink, error) {
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

// SessionLabel returns a short label for a session identified by iTerm2 session ID prefix.
type SessionLabel struct {
	ItermSession string `json:"iterm_session"`
	Workspace    string `json:"workspace"`
	Title        string `json:"title"`
}

// ResolveLabels looks up human-readable labels for a set of iTerm2 session ID prefixes.
func (s *Store) ResolveLabels(ctx context.Context, prefixes []string) (map[string]SessionLabel, error) {
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
func (s *Store) GraphData(ctx context.Context, since time.Time) (*cass.GraphData, error) {
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

// buildContent concatenates all message content for full-text indexing.
func buildContent(sess cass.Session) string {
	var b strings.Builder
	for _, msg := range sess.Messages {
		if msg.Content != "" {
			b.WriteString(msg.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
