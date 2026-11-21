package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

// session_skills holds one row per skill signal per session, the indexed form
// of sessions.skills_json. bare_name is the lowercased name without its plugin
// prefix, which is what lookups match. Rows are cleared and reinserted per
// session at index time, mirroring subagent_runs.
const sessionSkillsSchema = `
	CREATE TABLE IF NOT EXISTS session_skills (
		session_id TEXT NOT NULL,
		agent TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL,
		bare_name TEXT NOT NULL,
		kind TEXT NOT NULL,
		path TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT '',
		count INTEGER NOT NULL DEFAULT 0,
		first_seen INTEGER NOT NULL DEFAULT 0,
		last_seen INTEGER NOT NULL DEFAULT 0,
		evidence_json TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (session_id, name, kind, path)
	);
	CREATE INDEX IF NOT EXISTS idx_session_skills_bare ON session_skills(bare_name, kind);
	CREATE INDEX IF NOT EXISTS idx_session_skills_kind ON session_skills(kind, agent);
`

// skillsRefreshedMeta is the metadata key [DB.ReplaceSessionSkills] callers
// set after re-extracting skills from every session file.
const skillsRefreshedMeta = "skills_refreshed_at"

// bareSkillName returns name without its plugin prefix, lowercased.
func bareSkillName(name string) string {
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

const insertSessionSkillSQL = `
	INSERT OR REPLACE INTO session_skills (
		session_id, agent, name, bare_name, kind, path, source, count, first_seen, last_seen, evidence_json
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// writeSessionSkills replaces the session_skills rows for one session.
func writeSessionSkills(ctx context.Context, tx *sql.Tx, stmt *sql.Stmt, id, agent string, skills []cass.SkillUse) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_skills WHERE session_id = ?", id); err != nil {
		return fmt.Errorf("clear session_skills %s: %w", id, err)
	}
	for _, sk := range skills {
		if sk.Name == "" {
			continue
		}
		evidence, _ := json.Marshal(sk.Evidence)
		if _, err := stmt.ExecContext(ctx,
			id, agent, sk.Name, bareSkillName(sk.Name), sk.Kind, sk.Path, sk.Source, sk.Count,
			unixSeconds(sk.FirstSeen), unixSeconds(sk.LastSeen), string(evidence),
		); err != nil {
			return fmt.Errorf("insert session_skill %s/%s: %w", id, sk.Name, err)
		}
	}
	return nil
}

// unixSeconds is the inverse of unixOrZero.
func unixSeconds(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ReplaceSessionSkills rewrites the skill rows of sessions already in the
// index, leaving every other column alone, and returns how many sessions it
// updated. Sessions not yet indexed are skipped: they get their skills when
// they are indexed.
func (s *DB) ReplaceSessionSkills(ctx context.Context, sessions []cass.Session) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, insertSessionSkillSQL)
	if err != nil {
		return 0, fmt.Errorf("prepare session_skills: %w", err)
	}
	defer stmt.Close()

	n := 0
	for _, sess := range sessions {
		var agent string
		err := tx.QueryRowContext(ctx, "SELECT agent FROM sessions WHERE id = ?", sess.ID).Scan(&agent)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("look up session %s: %w", sess.ID, err)
		}
		if err := writeSessionSkills(ctx, tx, stmt, sess.ID, agent, sess.Skills); err != nil {
			return n, err
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return n, fmt.Errorf("commit: %w", err)
	}
	return n, nil
}

// SkillsRefreshedAt reports when skills were last re-extracted from every
// session file, or the zero time if they never were. Before that, sessions
// indexed by an older cass have no session_skills rows.
func (s *DB) SkillsRefreshedAt(ctx context.Context) (time.Time, error) {
	v, err := s.Meta(ctx, skillsRefreshedMeta)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	var unix int64
	if _, err := fmt.Sscan(v, &unix); err != nil {
		return time.Time{}, fmt.Errorf("parse %s: %w", skillsRefreshedMeta, err)
	}
	return time.Unix(unix, 0), nil
}

// SetSkillsRefreshedAt records a completed skill refresh.
func (s *DB) SetSkillsRefreshedAt(ctx context.Context, t time.Time) error {
	return s.SetMeta(ctx, skillsRefreshedMeta, fmt.Sprint(t.Unix()))
}

// skillNameMatch returns a condition on session_skills k matching pattern,
// as documented on [cass.SkillQuery].
func skillNameMatch(pattern string) (string, []any) {
	pattern = strings.TrimSpace(pattern)
	switch {
	case pattern == "":
		return "", nil
	case strings.Contains(pattern, "/"):
		return "k.path LIKE ?", []any{"%" + pattern + "%"}
	case strings.ContainsAny(pattern, "*?["):
		// A prefixed glob such as "anthropic-skills:*" must see the
		// prefix; an unprefixed one matches any plugin's skill.
		if strings.Contains(pattern, ":") {
			return "lower(k.name) GLOB ?", []any{strings.ToLower(pattern)}
		}
		return "k.bare_name GLOB ?", []any{strings.ToLower(pattern)}
	default:
		return "k.bare_name = ?", []any{bareSkillName(pattern)}
	}
}

// skillWhere returns the WHERE clause for q over session_skills k joined
// with sessions s.
func skillWhere(q cass.SkillQuery) (string, []any) {
	var where []string
	var args []any
	if cond, a := skillNameMatch(q.Skill); cond != "" {
		where = append(where, cond)
		args = append(args, a...)
	}
	kinds := q.Kinds
	if kinds == nil {
		kinds = cass.SkillUseKinds
	}
	if len(kinds) > 0 {
		where = append(where, "k.kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(kinds)), ",")+")")
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	if q.Agent != "" {
		where = append(where, agentFilter("k.agent"))
		args = append(args, agentFilterArgs(q.Agent)...)
	}
	if q.Workspace != "" {
		where = append(where, "s.workspace LIKE ?")
		args = append(args, "%"+q.Workspace+"%")
	}
	if !q.Since.IsZero() {
		// NAC records no message times, so fall back to the session's end.
		where = append(where, "COALESCE(NULLIF(k.last_seen, 0), s.ended_at) >= ?")
		args = append(args, q.Since.Unix())
	}
	if len(where) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(where, " AND "), args
}

// SkillSessions returns the sessions with skill signals matching q, most
// recent first. Limit counts sessions, not signals.
func (s *DB) SkillSessions(ctx context.Context, q cass.SkillQuery) ([]cass.SkillSession, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	where, args := skillWhere(q)
	seen := "MAX(COALESCE(NULLIF(k.last_seen, 0), s.ended_at))"
	query := `
		WITH hit AS (
			SELECT k.session_id, ` + seen + ` AS seen
			FROM session_skills k JOIN sessions s ON s.id = k.session_id
			` + where + `
			GROUP BY k.session_id
			ORDER BY seen DESC
			LIMIT ?
		)
		SELECT k.session_id, s.agent, s.title, s.workspace, s.source_path, s.started_at, s.ended_at, hit.seen,
			k.name, k.kind, k.path, k.source, k.count, k.first_seen, k.last_seen, k.evidence_json
		FROM hit
		JOIN session_skills k ON k.session_id = hit.session_id
		JOIN sessions s ON s.id = hit.session_id
		` + where + `
		ORDER BY hit.seen DESC, k.session_id, k.last_seen, k.name, k.kind`
	all := append(append(append([]any{}, args...), q.Limit), args...)
	rows, err := s.db.QueryContext(ctx, query, all...)
	if err != nil {
		return nil, fmt.Errorf("query skill sessions: %w", err)
	}
	defer rows.Close()

	var out []cass.SkillSession
	for rows.Next() {
		var ss cass.SkillSession
		var sk cass.SkillUse
		var started, ended, seenUnix, first, last int64
		var evidence string
		if err := rows.Scan(&ss.SessionID, &ss.Agent, &ss.Title, &ss.Workspace, &ss.SourcePath, &started, &ended, &seenUnix,
			&sk.Name, &sk.Kind, &sk.Path, &sk.Source, &sk.Count, &first, &last, &evidence); err != nil {
			return nil, fmt.Errorf("scan skill session: %w", err)
		}
		sk.FirstSeen, sk.LastSeen = unixOrZero(first), unixOrZero(last)
		_ = json.Unmarshal([]byte(evidence), &sk.Evidence)
		if n := len(out); n > 0 && out[n-1].SessionID == ss.SessionID {
			out[n-1].Skills = append(out[n-1].Skills, sk)
			continue
		}
		if started > 0 {
			ss.StartedAt = time.Unix(started, 0).Format(time.RFC3339)
		}
		if ended > 0 {
			ss.EndedAt = time.Unix(ended, 0).Format(time.RFC3339)
		}
		ss.LastSeen = unixOrZero(seenUnix)
		ss.Skills = []cass.SkillUse{sk}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// SkillStats summarizes the skills matching q by name, most-used first.
// Limit counts skills.
func (s *DB) SkillStats(ctx context.Context, q cass.SkillQuery) ([]cass.SkillStat, error) {
	where, args := skillWhere(q)
	from := "session_skills k"
	if q.Workspace != "" || !q.Since.IsZero() {
		from += " JOIN sessions s ON s.id = k.session_id"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT k.bare_name, k.agent, COUNT(DISTINCT k.session_id),
			COALESCE(MIN(NULLIF(k.first_seen, 0)), 0), MAX(k.last_seen)
		FROM `+from+`
		`+where+`
		GROUP BY k.bare_name, k.agent`, args...)
	if err != nil {
		return nil, fmt.Errorf("query skill stats: %w", err)
	}
	defer rows.Close()

	byName := map[string]*cass.SkillStat{}
	for rows.Next() {
		var name, agent string
		var sessions int
		var first, last int64
		if err := rows.Scan(&name, &agent, &sessions, &first, &last); err != nil {
			return nil, fmt.Errorf("scan skill stat: %w", err)
		}
		st := byName[name]
		if st == nil {
			st = &cass.SkillStat{Name: name, Agents: map[string]int{}}
			byName[name] = st
		}
		// A session has one agent, so per-agent counts sum exactly.
		st.Sessions += sessions
		st.Agents[agent] += sessions
		if f := unixOrZero(first); !f.IsZero() && (st.FirstSeen.IsZero() || f.Before(st.FirstSeen)) {
			st.FirstSeen = f
		}
		if l := unixOrZero(last); l.After(st.LastSeen) {
			st.LastSeen = l
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]cass.SkillStat, 0, len(byName))
	for _, st := range byName {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Name < out[j].Name
	})
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// attachSessionSkills replaces each hit's skills with its session_skills
// rows, when it has any. Those rows are newer than sessions.skills_json for
// sessions whose skills were refreshed without being reindexed. Summary hits
// keep only the skills that count as use, as scanSummaryHit does.
func (s *DB) attachSessionSkills(ctx context.Context, hits []cass.Hit, summary bool) error {
	if len(hits) == 0 {
		return nil
	}
	args := make([]any, len(hits))
	byID := make(map[string][]int, len(hits))
	for i, h := range hits {
		args[i] = h.SessionID
		byID[h.SessionID] = append(byID[h.SessionID], i)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, name, kind, path, source, count, first_seen, last_seen, evidence_json
		FROM session_skills
		WHERE session_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+`)
		ORDER BY session_id, last_seen, name, kind`, args...)
	if err != nil {
		return fmt.Errorf("query hit skills: %w", err)
	}
	defer rows.Close()
	skills := map[string][]cass.SkillUse{}
	for rows.Next() {
		var id, evidence string
		var sk cass.SkillUse
		var first, last int64
		if err := rows.Scan(&id, &sk.Name, &sk.Kind, &sk.Path, &sk.Source, &sk.Count, &first, &last, &evidence); err != nil {
			return fmt.Errorf("scan hit skill: %w", err)
		}
		sk.FirstSeen, sk.LastSeen = unixOrZero(first), unixOrZero(last)
		_ = json.Unmarshal([]byte(evidence), &sk.Evidence)
		skills[id] = append(skills[id], sk)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, list := range skills {
		for _, i := range byID[id] {
			h := &hits[i]
			h.SkillCount, h.SelectedSkillCount, h.LoadedSkillCount = skillCounts(list)
			if summary {
				h.Skills = summarySkillUses(list)
			} else {
				h.Skills = list
			}
		}
	}
	return nil
}
