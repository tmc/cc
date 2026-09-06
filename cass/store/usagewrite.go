package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// usageRollupNoticeRows is the event count above which the one-time rollup fill
// announces itself. The fill runs at about a million rows a second, so this is
// where it starts to be noticeable.
const usageRollupNoticeRows = 1_000_000

// fillUsageRollup populates usage_hourly from usage_events when the rollup is
// empty and the events are not. Unlike usage_events itself, which only a
// reindex can fill, the rollup is derived, so an index that predates it is
// brought up to date at open rather than reading as though nothing happened.
func (s *DB) fillUsageRollup() error {
	var events, rollup int
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM usage_events), (SELECT count(*) FROM usage_hourly)").
		Scan(&events, &rollup); err != nil {
		return fmt.Errorf("usage rollup state: %w", err)
	}
	if events == 0 || rollup > 0 {
		return nil
	}
	// A grouped scan of the fact table, which on a corpus-sized index takes
	// long enough that a silent one reads as a hang.
	if events >= usageRollupNoticeRows {
		fmt.Fprintf(os.Stderr, "cass: filling usage rollup from %d events\n", events)
	}
	if _, err := s.db.Exec(`
		INSERT INTO usage_hourly (
			hour, agent, vendor, model, git_common_dir, is_sidechain,
			requests, estimated,
			input_tokens, cache_read_tokens, cache_creation_tokens, output_tokens
		)
		SELECT ts/3600, agent, vendor, model, git_common_dir, is_sidechain,
		       count(*), sum(output_estimated),
		       sum(input_tokens), sum(cache_read_tokens),
		       sum(cache_creation_tokens), sum(output_tokens)
		FROM usage_events GROUP BY 1, 2, 3, 4, 5, 6`); err != nil {
		return fmt.Errorf("fill usage rollup: %w", err)
	}
	return nil
}

// usageHour is the unix hour a usage row falls in.
func usageHour(ts int64) int64 { return ts / 3600 }

// sessionUsageHours returns the hours a session's stored usage rows touch, so
// that the rollup can be recomputed for exactly those hours after the rows are
// replaced.
func sessionUsageHours(ctx context.Context, tx *sql.Tx, sessionID string, hours map[int64]bool) error {
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT ts/3600 FROM usage_events WHERE session_id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("usage hours %s: %w", sessionID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return err
		}
		hours[h] = true
	}
	return rows.Err()
}

// agentUsageHours returns the hours the usage rows of one harness's sessions
// touch.
func agentUsageHours(ctx context.Context, tx *sql.Tx, agent string, hours map[int64]bool) error {
	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT ts/3600 FROM usage_events WHERE session_id IN (SELECT id FROM sessions WHERE "+agentFilter("agent")+")",
		agentFilterArgs(agent)...)
	if err != nil {
		return fmt.Errorf("usage hours for %s: %w", agent, err)
	}
	defer rows.Close()
	for rows.Next() {
		var h int64
		if err := rows.Scan(&h); err != nil {
			return err
		}
		hours[h] = true
	}
	return rows.Err()
}

// claimRequest reports whether the session identified by (startedAt, id) owns
// the request, deleting the rows of any session it outranks.
//
// A forked or resumed session opens with a verbatim copy of its ancestor's
// transcript, the ancestor's request ids included, so the same request is
// present in two files and would be counted twice. The request belongs to the
// session with the smaller (started_at, id) — the ancestor, and its own id as
// the tie-break — which is a function of the sessions themselves and not of
// the order they were indexed in, so both orders converge on the same table.
//
// An empty request id identifies nothing and never participates: Codex records
// no per-request id and its copies are dropped structurally by the collector.
// Request ids are matched within one harness, since a copy never crosses one.
func claimRequest(ctx context.Context, tx *sql.Tx, agent, requestID string, startedAt int64, id string, hours map[int64]bool) (bool, error) {
	if requestID == "" {
		return true, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT e.ts, s.started_at, s.id
		FROM usage_events e JOIN sessions s ON s.id = e.session_id
		WHERE e.agent = ? AND e.request_id = ?
	`, agent, requestID)
	if err != nil {
		return false, fmt.Errorf("claim request %s: %w", requestID, err)
	}
	type held struct {
		ts        int64
		startedAt int64
		id        string
	}
	var holders []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.ts, &h.startedAt, &h.id); err != nil {
			rows.Close()
			return false, err
		}
		holders = append(holders, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, h := range holders {
		if h.startedAt < startedAt || (h.startedAt == startedAt && h.id < id) {
			return false, nil
		}
	}
	for _, h := range holders {
		hours[usageHour(h.ts)] = true
	}
	if len(holders) > 0 {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM usage_events WHERE agent = ? AND request_id = ?", agent, requestID); err != nil {
			return false, fmt.Errorf("drop duplicate request %s: %w", requestID, err)
		}
	}
	return true, nil
}

// recomputeUsageHours rebuilds the rollup rows for the given hours from
// usage_events. Rows are recomputed rather than adjusted by a delta, so the
// rollup cannot drift from the fact table however a session's rows changed.
func recomputeUsageHours(ctx context.Context, tx *sql.Tx, hours map[int64]bool) error {
	for h := range hours {
		if _, err := tx.ExecContext(ctx, "DELETE FROM usage_hourly WHERE hour = ?", h); err != nil {
			return fmt.Errorf("clear usage_hourly %d: %w", h, err)
		}
		// Bounded by timestamp rather than by ts/3600 so the ts index applies.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO usage_hourly (
				hour, agent, vendor, model, git_common_dir, is_sidechain,
				requests, estimated,
				input_tokens, cache_read_tokens, cache_creation_tokens, output_tokens
			)
			SELECT ts/3600, agent, vendor, model, git_common_dir, is_sidechain,
			       count(*), sum(output_estimated),
			       sum(input_tokens), sum(cache_read_tokens),
			       sum(cache_creation_tokens), sum(output_tokens)
			FROM usage_events WHERE ts >= ? AND ts < ?
			GROUP BY 1, 2, 3, 4, 5, 6
		`, h*3600, (h+1)*3600); err != nil {
			return fmt.Errorf("fill usage_hourly %d: %w", h, err)
		}
	}
	return nil
}
