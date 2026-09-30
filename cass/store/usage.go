package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// usageSchema holds the per-request usage fact table and the repository
// dimension it joins to. Rows are cleared and reinserted per session at index
// time, mirroring subagent_runs and workflows, so re-indexing a session twice
// leaves the table unchanged.
const usageSchema = `
	CREATE TABLE IF NOT EXISTS usage_events (
		session_id TEXT NOT NULL,
		agent TEXT NOT NULL,
		ts INTEGER NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		vendor TEXT NOT NULL DEFAULT '',
		git_common_dir TEXT NOT NULL DEFAULT '',
		attribution TEXT NOT NULL DEFAULT '',
		is_sidechain INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		output_estimated INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
	);
	-- ts alone. The write path recomputes the rollups an hour at a time from
	-- this range, and the queries that still read the fact table -- a session
	-- filter, a window edge the rollup cannot divide -- bound it the same way.
	-- Carrying session_id as a second column made the old session-span scan
	-- index-only, which the per-session rollup has since made unnecessary;
	-- that index cost 95 MB on a corpus-sized index against this one's 30 MB.
	CREATE INDEX IF NOT EXISTS idx_usage_ts ON usage_events(ts);
	DROP INDEX IF EXISTS idx_usage_ts_session;
	CREATE INDEX IF NOT EXISTS idx_usage_session ON usage_events(session_id);

	CREATE TABLE IF NOT EXISTS usage_hourly (
		hour INTEGER NOT NULL,
		agent TEXT NOT NULL,
		vendor TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		git_common_dir TEXT NOT NULL DEFAULT '',
		is_sidechain INTEGER NOT NULL DEFAULT 0,
		requests INTEGER NOT NULL DEFAULT 0,
		estimated INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (hour, agent, vendor, model, git_common_dir, is_sidechain)
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS idx_usage_hourly_hour ON usage_hourly(hour);

	-- The same rollup carried one dimension further, by session. Every report
	-- counts the sessions in its window and measures the longest, which on the
	-- fact table is a scan of the window's every request; here it is a scan of
	-- one row per session and hour, a few tens of thousands for a corpus.
	-- first_ts and last_ts keep the extent exact, so the answer is the one the
	-- events give rather than an hour-rounded approximation of it.
	CREATE TABLE IF NOT EXISTS usage_session_hourly (
		hour INTEGER NOT NULL,
		session_id TEXT NOT NULL,
		agent TEXT NOT NULL,
		vendor TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		git_common_dir TEXT NOT NULL DEFAULT '',
		attribution TEXT NOT NULL DEFAULT '',
		is_sidechain INTEGER NOT NULL DEFAULT 0,
		requests INTEGER NOT NULL DEFAULT 0,
		estimated INTEGER NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		first_ts INTEGER NOT NULL DEFAULT 0,
		last_ts INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (hour, session_id, agent, vendor, model, git_common_dir, attribution, is_sidechain)
	) WITHOUT ROWID;
	CREATE INDEX IF NOT EXISTS idx_usage_session_hourly_hour ON usage_session_hourly(hour);


	CREATE TABLE IF NOT EXISTS repos (
		git_common_dir TEXT PRIMARY KEY,
		remote TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		resolved_at INTEGER NOT NULL DEFAULT 0
	);
`

// Repo is one repository the index has attributed usage to. Remote is the
// normalized origin ("github.com/tmc/cc") and Name the display name; both are
// empty when the repository was no longer on disk when it was last seen.
// ResolvedAt is zero until the path resolves.
type Repo struct {
	GitCommonDir string `json:"git_common_dir"`
	Remote       string `json:"remote"`
	Name         string `json:"name"`
	ResolvedAt   int64  `json:"resolved_at"`
}

// UsageFilter restricts a usage query. The zero value selects everything.
// After is inclusive and Before exclusive; a zero time means unbounded.
type UsageFilter struct {
	After, Before    time.Time
	Agent            string
	Repo             string
	Model            string
	Session          string
	ExcludeSidechain bool
}

// where renders the filter as a SQL condition and its arguments.
func (f UsageFilter) where() (string, []any) {
	conds := []string{"1=1"}
	var args []any
	if !f.After.IsZero() {
		conds, args = append(conds, "ts >= ?"), append(args, f.After.Unix())
	}
	if !f.Before.IsZero() {
		conds, args = append(conds, "ts < ?"), append(args, f.Before.Unix())
	}
	if f.Agent != "" {
		// Matched as a family, as every other agent dimension in this
		// package is, so that "codex" selects codex-cli and codex-app.
		conds, args = append(conds, agentFilter("agent")), append(args, agentFilterArgs(f.Agent)...)
	}
	if f.Repo != "" {
		conds, args = append(conds, "git_common_dir = ?"), append(args, f.Repo)
	}
	if f.Model != "" {
		conds, args = append(conds, "model = ?"), append(args, f.Model)
	}
	if f.Session != "" {
		conds, args = append(conds, "session_id = ?"), append(args, f.Session)
	}
	if f.ExcludeSidechain {
		conds = append(conds, "is_sidechain = 0")
	}
	return strings.Join(conds, " AND "), args
}

// rollup reports whether the filter can be answered from usage_hourly. Every
// dimension the rollup is keyed by is a filter column; the session is not, so a
// session filter reads the fact table instead. A window edge that falls inside
// an hour does too: the rollup cannot divide one, and rounding it either way
// would make this view disagree with the ones that read the events. Report
// windows are midnight boundaries, which are hour-aligned in every zone whose
// offset is a whole number of hours.
func (f UsageFilter) rollup() bool {
	return f.Session == "" && hourAligned(f.After) && hourAligned(f.Before)
}

// hourAligned reports whether t is zero, meaning unbounded, or lands exactly on
// an hour.
func hourAligned(t time.Time) bool { return t.IsZero() || t.Unix()%3600 == 0 }

// hourWhere renders the filter against usage_hourly, whose time column is the
// hour rather than the timestamp. It is only called for a filter [UsageFilter.rollup]
// accepted, so both edges land on an hour and select exactly the same requests
// the equivalent timestamp bounds would.
func (f UsageFilter) hourWhere() (string, []any) {
	conds := []string{"1=1"}
	var args []any
	if !f.After.IsZero() {
		conds, args = append(conds, "hour >= ?"), append(args, f.After.Unix()/3600)
	}
	if !f.Before.IsZero() {
		conds, args = append(conds, "hour < ?"), append(args, f.Before.Unix()/3600)
	}
	if f.Agent != "" {
		conds, args = append(conds, agentFilter("agent")), append(args, agentFilterArgs(f.Agent)...)
	}
	if f.Repo != "" {
		conds, args = append(conds, "git_common_dir = ?"), append(args, f.Repo)
	}
	if f.Model != "" {
		conds, args = append(conds, "model = ?"), append(args, f.Model)
	}
	if f.ExcludeSidechain {
		conds = append(conds, "is_sidechain = 0")
	}
	return strings.Join(conds, " AND "), args
}

// HourRow is the usage of one UTC hour. Callers map Hour into a display zone
// themselves, so that day and weekday bucketing is correct in every zone and
// across daylight-saving transitions.
type HourRow struct {
	Hour                                  int64 `json:"hour"` // unix hour, ts/3600
	Requests                              int   `json:"requests"`
	Input, CacheRead, CacheCreate, Output int
	Estimated                             int `json:"estimated"` // requests whose output was estimated
}

// GroupRow is the usage of one (repo, agent, vendor, model) combination.
type GroupRow struct {
	Repo, Agent, Vendor, Model            string
	Sessions, Requests                    int
	First, Last                           time.Time
	Input, CacheRead, CacheCreate, Output int
	Estimated                             int
}

// SessionSpanRow is one session's extent within a filter, for the session
// count and the longest-session figure reports print.
type SessionSpanRow struct {
	SessionID                             string    `json:"session_id"`
	First, Last                           time.Time `json:"-"`
	Requests                              int       `json:"requests"`
	Input, CacheRead, CacheCreate, Output int
}

// Duration returns how long the session's requests spanned.
func (r SessionSpanRow) Duration() time.Duration { return r.Last.Sub(r.First) }

// UsageByHour returns one row per UTC hour with usage in it, ordered by hour.
//
// It reads the usage_hourly rollup, which is keyed by the same dimensions the
// filter selects on and is maintained with the fact table, so the sums are
// equal. A session filter has no rollup column and falls back to the events.
func (s *DB) UsageByHour(ctx context.Context, f UsageFilter) ([]HourRow, error) {
	if f.rollup() {
		return s.usageByHourRollup(ctx, f)
	}
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts / 3600 AS hour, count(*),
			coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
			coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0),
			coalesce(sum(output_estimated), 0)
		FROM usage_events WHERE `+where+`
		GROUP BY hour ORDER BY hour`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by hour: %w", err)
	}
	defer rows.Close()

	var result []HourRow
	for rows.Next() {
		var r HourRow
		if err := rows.Scan(&r.Hour, &r.Requests, &r.Input, &r.CacheRead,
			&r.CacheCreate, &r.Output, &r.Estimated); err != nil {
			return nil, fmt.Errorf("scan usage hour: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// usageByHourRollup answers UsageByHour from usage_hourly.
func (s *DB) usageByHourRollup(ctx context.Context, f UsageFilter) ([]HourRow, error) {
	where, args := f.hourWhere()
	rows, err := s.db.QueryContext(ctx, `
		SELECT hour, coalesce(sum(requests), 0),
			coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
			coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0),
			coalesce(sum(estimated), 0)
		FROM usage_hourly WHERE `+where+`
		GROUP BY hour ORDER BY hour`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by hour: %w", err)
	}
	defer rows.Close()

	var result []HourRow
	for rows.Next() {
		var r HourRow
		if err := rows.Scan(&r.Hour, &r.Requests, &r.Input, &r.CacheRead,
			&r.CacheCreate, &r.Output, &r.Estimated); err != nil {
			return nil, fmt.Errorf("scan usage hour: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// usageByGroupRollup answers UsageByGroup from usage_hourly. The rollup holds
// no session id, so Sessions is zero — every caller that prints a session count
// takes it from [DB.UsageSessionCounts] — and First and Last are the bounds of
// the hours the group appears in rather than of its own requests.
func (s *DB) usageByGroupRollup(ctx context.Context, f UsageFilter) ([]GroupRow, error) {
	where, args := f.hourWhere()
	rows, err := s.db.QueryContext(ctx, `
		SELECT git_common_dir, agent, vendor, model,
			coalesce(sum(requests), 0), min(hour), max(hour),
			coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
			coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0),
			coalesce(sum(estimated), 0)
		FROM usage_hourly WHERE `+where+`
		GROUP BY 1, 2, 3, 4
		ORDER BY sum(input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by group: %w", err)
	}
	defer rows.Close()

	var result []GroupRow
	for rows.Next() {
		var r GroupRow
		var first, last int64
		if err := rows.Scan(&r.Repo, &r.Agent, &r.Vendor, &r.Model,
			&r.Requests, &first, &last,
			&r.Input, &r.CacheRead, &r.CacheCreate, &r.Output, &r.Estimated); err != nil {
			return nil, fmt.Errorf("scan usage group: %w", err)
		}
		r.First, r.Last = time.Unix(first*3600, 0).UTC(), time.Unix(last*3600+3599, 0).UTC()
		result = append(result, r)
	}
	return result, rows.Err()
}

// UsageByGroup returns one row per (repo, agent, vendor, model), ordered by
// total tokens descending. Views that want a coarser split, such as agent by
// vendor, fold the token and request columns in Go so that every view sums to
// the same totals. Sessions is a distinct count and must not be summed: use
// [DB.UsageSessionCounts] with the coarser dimension instead.
func (s *DB) UsageByGroup(ctx context.Context, f UsageFilter) ([]GroupRow, error) {
	if f.rollup() {
		return s.usageByGroupRollup(ctx, f)
	}
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT git_common_dir, agent, vendor, model,
			count(DISTINCT session_id), count(*), min(ts), max(ts),
			coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
			coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0),
			coalesce(sum(output_estimated), 0)
		FROM usage_events WHERE `+where+`
		GROUP BY 1, 2, 3, 4
		ORDER BY sum(input_tokens + cache_read_tokens + cache_creation_tokens + output_tokens) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by group: %w", err)
	}
	defer rows.Close()

	var result []GroupRow
	for rows.Next() {
		var r GroupRow
		var first, last int64
		if err := rows.Scan(&r.Repo, &r.Agent, &r.Vendor, &r.Model,
			&r.Sessions, &r.Requests, &first, &last,
			&r.Input, &r.CacheRead, &r.CacheCreate, &r.Output, &r.Estimated); err != nil {
			return nil, fmt.Errorf("scan usage group: %w", err)
		}
		r.First, r.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		result = append(result, r)
	}
	return result, rows.Err()
}

// UsageSessions returns one row per session with usage under the filter,
// ordered by first request. A session that spans the window boundary is
// reported by the part of it inside the window, since that is the part the
// surrounding report covers.
func (s *DB) UsageSessions(ctx context.Context, f UsageFilter) ([]SessionSpanRow, error) {
	if !f.rollup() {
		return s.usageSessionsFromEvents(ctx, f)
	}
	// The per-session rollup keeps each session's true first and last
	// timestamp, so this is the same answer read from a table a couple of
	// orders of magnitude smaller than the window's requests.
	where, args := f.hourWhere()
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, min(first_ts), max(last_ts), sum(requests),
coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0)
		FROM usage_session_hourly WHERE `+where+`
		GROUP BY session_id ORDER BY 2`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	defer rows.Close()
	return scanSessionSpans(rows)
}

// scanSessionSpans reads session extents, requests, and token totals.
func scanSessionSpans(rows *sql.Rows) ([]SessionSpanRow, error) {
	var result []SessionSpanRow
	for rows.Next() {
		var r SessionSpanRow
		var first, last int64
		if err := rows.Scan(&r.SessionID, &first, &last, &r.Requests, &r.Input, &r.CacheRead, &r.CacheCreate, &r.Output); err != nil {
			return nil, fmt.Errorf("scan usage session: %w", err)
		}
		r.First, r.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		result = append(result, r)
	}
	return result, rows.Err()
}

// usageSessionsFromEvents answers UsageSessions from the fact table, which is
// where a session-filtered query goes and what the rollup is checked against.
func (s *DB) usageSessionsFromEvents(ctx context.Context, f UsageFilter) ([]SessionSpanRow, error) {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, min(ts), max(ts), count(*),
coalesce(sum(input_tokens), 0), coalesce(sum(cache_read_tokens), 0),
coalesce(sum(cache_creation_tokens), 0), coalesce(sum(output_tokens), 0)
		FROM usage_events WHERE `+where+`
		GROUP BY session_id ORDER BY 2`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage sessions: %w", err)
	}
	defer rows.Close()
	return scanSessionSpans(rows)
}

// UsageCoverage reports whether the index holds any usage rows, and the
// earliest one. Both are empty on an index that predates the table, which is
// what the commands warn about: a schema migration adds the table empty and
// only a reindex fills it.
//
// The question is whether there are rows, not how many, so this reads the
// smallest value of the timestamp index rather than counting: every report
// calls it, and a count is a scan of the whole fact table.
func (s *DB) UsageCoverage(ctx context.Context) (any_ bool, oldest time.Time, err error) {
	var ts int64
	row := s.db.QueryRowContext(ctx, `SELECT coalesce(min(ts), 0) FROM usage_events`)
	if err := row.Scan(&ts); err != nil {
		return false, time.Time{}, fmt.Errorf("usage coverage: %w", err)
	}
	if ts == 0 {
		return false, time.Time{}, nil
	}
	return true, time.Unix(ts, 0).UTC(), nil
}

// Repos returns every known repository keyed by its git common dir.
func (s *DB) Repos(ctx context.Context) (map[string]Repo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT git_common_dir, remote, name, resolved_at FROM repos`)
	if err != nil {
		return nil, fmt.Errorf("repos: %w", err)
	}
	defer rows.Close()

	repos := make(map[string]Repo)
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.GitCommonDir, &r.Remote, &r.Name, &r.ResolvedAt); err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		repos[r.GitCommonDir] = r
	}
	return repos, rows.Err()
}

// SaveRepos upserts repository records.
func (s *DB) SaveRepos(ctx context.Context, repos []Repo) error {
	if len(repos) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO repos (git_common_dir, remote, name, resolved_at)
		VALUES (?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare repos: %w", err)
	}
	defer stmt.Close()
	for _, r := range repos {
		if r.GitCommonDir == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, r.GitCommonDir, r.Remote, r.Name, r.ResolvedAt); err != nil {
			return fmt.Errorf("insert repo %s: %w", r.GitCommonDir, err)
		}
	}
	return tx.Commit()
}

// WorktreePaths returns the (worktree path -> git common dir) map the index
// has learned from sessions whose git context did resolve. Attribution uses it
// to place a working directory that is a subdirectory of a known checkout, or
// one whose repository has since been deleted.
//
// One workspace can carry several common dirs — nested repositories, or a
// checkout replaced by a different repository. The choice must be a function
// of the row set and not of the order SQLite returns it in, or attribution
// moves tokens between repositories on re-index, so the rows are ordered and
// the last one wins: the most specific dir, then the lexically greatest.
func (s *DB) WorktreePaths(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT workspace, git_common_dir FROM sessions
		WHERE workspace != '' AND git_common_dir != ''
		ORDER BY workspace, length(git_common_dir), git_common_dir`)
	if err != nil {
		return nil, fmt.Errorf("worktree paths: %w", err)
	}
	defer rows.Close()

	paths := make(map[string]string)
	for rows.Next() {
		var workspace, dir string
		if err := rows.Scan(&workspace, &dir); err != nil {
			return nil, fmt.Errorf("scan worktree path: %w", err)
		}
		paths[workspace] = dir
	}
	return paths, rows.Err()
}

// boolInt renders a bool as the integer SQLite stores it as.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// usageDims are the columns UsageSessionCounts may group by. The value is
// interpolated into SQL, so it is checked against this set rather than bound.
var usageDims = map[string]bool{
	"git_common_dir": true,
	"agent":          true,
	"vendor":         true,
	"model":          true,
	"attribution":    true,
	"session_id":     true,
}

// UsageRowCounts returns the number of usage rows per value of dim, which must
// name a usage_events dimension column.
func (s *DB) UsageRowCounts(ctx context.Context, f UsageFilter, dim string) (map[string]int, error) {
	if !usageDims[dim] {
		return nil, fmt.Errorf("usage counts: unknown dimension %q", dim)
	}
	if !f.rollup() {
		return s.usageCounts(ctx, f, dim, "count(*)")
	}
	where, args := f.hourWhere()
	return s.scanCounts(ctx, `
		SELECT `+dim+`, sum(requests)
		FROM usage_session_hourly WHERE `+where+`
		GROUP BY 1`, args)
}

// UsageSessionCounts returns the number of distinct sessions per value of dim,
// which must name a usage_events dimension column. Summing the per-group
// counts from UsageByGroup would count a session once per model it used.
func (s *DB) UsageSessionCounts(ctx context.Context, f UsageFilter, dim string) (map[string]int, error) {
	if !usageDims[dim] {
		return nil, fmt.Errorf("usage counts: unknown dimension %q", dim)
	}
	if !f.rollup() {
		return s.usageCounts(ctx, f, dim, "count(DISTINCT session_id)")
	}
	// The per-session rollup is keyed by the same dimensions and carries the
	// session, so the distinct count is the same one, over far fewer rows.
	where, args := f.hourWhere()
	return s.scanCounts(ctx, `
		SELECT `+dim+`, count(DISTINCT session_id)
		FROM usage_session_hourly WHERE `+where+`
		GROUP BY 1`, args)
}

// usageCounts groups agg by dim under the filter, over the fact table.
func (s *DB) usageCounts(ctx context.Context, f UsageFilter, dim, agg string) (map[string]int, error) {
	if !usageDims[dim] {
		return nil, fmt.Errorf("usage counts: unknown dimension %q", dim)
	}
	where, args := f.where()
	return s.scanCounts(ctx, `
		SELECT `+dim+`, `+agg+`
		FROM usage_events WHERE `+where+`
		GROUP BY 1`, args)
}

// scanCounts runs a two-column key/count query.
func (s *DB) scanCounts(ctx context.Context, query string, args []any) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage counts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, fmt.Errorf("scan usage count: %w", err)
		}
		out[key] = n
	}
	return out, rows.Err()
}

// OldestSessionStart returns the start of the earliest indexed session, or the
// zero time when the index is empty.
func (s *DB) OldestSessionStart(ctx context.Context) (time.Time, error) {
	var ts sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT min(started_at) FROM sessions WHERE started_at > 0").Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("oldest session: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return time.Unix(ts.Int64, 0).UTC(), nil
}

// SessionUsageRow is one session's usage within a filter.
type SessionUsageRow struct {
	SessionID                             string
	Agent, Repo                           string
	Requests                              int
	First, Last                           time.Time
	Input, CacheRead, CacheCreate, Output int
	Estimated                             int
}

// UsageBySession returns one row per session in the filter, ordered by total
// tokens, largest first. It answers what share of a window one session was.
func (s *DB) UsageBySession(ctx context.Context, f UsageFilter) ([]SessionUsageRow, error) {
	// The per-session rollup holds the same sums keyed by session and hour,
	// so a report window reads it rather than the window's every request.
	table, first, last, requests, est := "usage_events", "min(ts)", "max(ts)", "count(*)", "output_estimated"
	where, args := f.where()
	if f.rollup() {
		table, first, last = "usage_session_hourly", "min(first_ts)", "max(last_ts)"
		requests, est = "sum(requests)", "estimated"
		where, args = f.hourWhere()
	}
	sum := func(col string) string { return "coalesce(sum(" + col + "), 0)" }
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, agent, git_common_dir, `+requests+`, `+first+`, `+last+`,
			`+sum("input_tokens")+`, `+sum("cache_read_tokens")+`,
			`+sum("cache_creation_tokens")+`, `+sum("output_tokens")+`,
			`+sum(est)+`
		FROM `+table+` WHERE `+where+`
		GROUP BY 1
		ORDER BY sum(input_tokens) + sum(cache_read_tokens) +
			sum(cache_creation_tokens) + sum(output_tokens) DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage by session: %w", err)
	}
	defer rows.Close()
	var out []SessionUsageRow
	for rows.Next() {
		var r SessionUsageRow
		var first, last int64
		if err := rows.Scan(&r.SessionID, &r.Agent, &r.Repo, &r.Requests, &first, &last,
			&r.Input, &r.CacheRead, &r.CacheCreate, &r.Output, &r.Estimated); err != nil {
			return nil, fmt.Errorf("usage by session: %w", err)
		}
		r.First, r.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
