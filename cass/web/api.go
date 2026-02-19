package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := cass.SearchRequest{
		Query: q.Get("q"),
		Filters: cass.Filters{
			Agent:     q.Get("agent"),
			Workspace: q.Get("workspace"),
			Team:      q.Get("team"),
		},
	}

	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Limit = n
		}
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			req.Offset = n
		}
	}
	if v := q.Get("sort"); v != "" {
		req.Sort = cass.SortMode(v)
	}
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			req.Filters.After = time.Now().Add(-d)
		}
	}
	if v := q.Get("after"); v != "" {
		if t, err := parseTime(v); err == nil {
			req.Filters.After = t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := parseTime(v); err == nil {
			req.Filters.Before = t
		}
	}

	result, err := s.svc.Search(r.Context(), req)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// If aggregate=true or any time filters, return detailed aggregate stats.
	if q.Get("aggregate") == "true" || q.Get("since") != "" || q.Get("after") != "" {
		s.handleAggregateStats(w, r)
		return
	}

	stats, err := s.svc.Stats(r.Context())
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleAggregateStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var after, before time.Time
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			after = time.Now().Add(-d)
		}
	}
	if v := q.Get("after"); v != "" {
		if t, err := parseTime(v); err == nil {
			after = t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := parseTime(v); err == nil {
			before = t
		}
	}

	stats, err := s.svc.AggregateStats(r.Context(), after, before)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	links, err := s.svc.Links(r.Context(), sessionID)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	// Filter by since if provided.
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cutoff := time.Now().Add(-d)
			var filtered []cass.SessionLink
			for _, l := range links {
				if l.Timestamp != "" {
					t, err := time.Parse("2006-01-02T15:04:05Z07:00", l.Timestamp)
					if err == nil && t.Before(cutoff) {
						continue
					}
				}
				filtered = append(filtered, l)
			}
			links = filtered
		}
	}

	writeJSON(w, links)
}

func (s *Server) handleMappings(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("filter")
	mappings, err := s.svc.Mappings(r.Context(), filter)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, mappings)
}

func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	ids := r.URL.Query().Get("ids")
	if ids == "" {
		writeJSON(w, map[string]any{})
		return
	}
	prefixes := strings.Split(ids, ",")
	labels, err := s.svc.ResolveLabels(r.Context(), prefixes)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, labels)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	skipArtifacts := r.URL.Query().Get("skip_artifacts") == "true"

	start := time.Now()
	count, err := s.svc.Index(r.Context(), force)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	artifactCount := 0
	if !skipArtifacts {
		// Non-fatal: artifact dirs may not exist.
		artifactCount, _ = s.svc.IndexArtifactDirs(r.Context(), "")
	}
	elapsed := time.Since(start)

	result := map[string]any{
		"indexed":          count,
		"artifact_requests": artifactCount,
		"duration_ms":      elapsed.Milliseconds(),
	}

	// Publish SSE event.
	s.broker.Publish(Event{
		Type: "index_complete",
		Data: result,
	})

	writeJSON(w, result)
}

// handleSessionStream streams a session's JSONL entries as SSE events.
// This enables real-time rendering via xterm.js in the browser.
func (s *Server) handleSessionStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, fmt.Errorf("missing session id"), http.StatusBadRequest)
		return
	}

	// Look up the session's source path directly by ID.
	sourcePath, err := s.svc.GetSourcePath(r.Context(), id)
	if err != nil || sourcePath == "" {
		writeError(w, fmt.Errorf("session not found: %s", id), http.StatusNotFound)
		return
	}

	// Check if client wants SSE streaming or JSON dump.
	if r.Header.Get("Accept") == "text/event-stream" {
		s.streamSession(w, r, sourcePath)
		return
	}

	// Default: return all entries as JSON array.
	entries, err := readSessionEntries(sourcePath)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

func (s *Server) streamSession(w http.ResponseWriter, r *http.Request, path string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	entries, err := readSessionEntries(path)
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
		flusher.Flush()
		return
	}

	ctx := r.Context()
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		data, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: entry\ndata: %s\n\n", data)
		flusher.Flush()

		// Small delay for visual streaming effect.
		time.Sleep(10 * time.Millisecond)
	}

	fmt.Fprintf(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}

func readSessionEntries(path string) ([]cc.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []cc.Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var entry cc.Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// readSessionEntries could also use cc.ReadFile but we inline to avoid
// potential issues with the large buffer in different contexts.

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error, status int) {
	// Surface SQLITE_BUSY as 503 so the UI can retry.
	msg := err.Error()
	if strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format: %s", s)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Optional time filter.
	var since time.Time
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			since = time.Now().Add(-d)
		}
	}
	if v := q.Get("after"); v != "" {
		if t, err := parseTime(v); err == nil {
			since = t
		}
	}

	graph, err := s.svc.Graph(r.Context(), since)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, graph)
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := cc.ListTeams()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	type memberInfo struct {
		Name      string `json:"name"`
		AgentType string `json:"agent_type"`
		Model     string `json:"model,omitempty"`
		Color     string `json:"color,omitempty"`
	}
	type teamInfo struct {
		Name        string       `json:"name"`
		Description string       `json:"description,omitempty"`
		CreatedAt   int64        `json:"created_at"`
		Lead        string       `json:"lead"`
		MemberCount int          `json:"member_count"`
		Members     []memberInfo `json:"members"`
	}

	var result []teamInfo
	for _, name := range teams {
		cfg, err := cc.ReadTeamConfig(name)
		if err != nil {
			continue
		}
		ti := teamInfo{
			Name:        cfg.Name,
			Description: cfg.Description,
			CreatedAt:   cfg.CreatedAt,
			Lead:        cfg.LeadAgentID,
			MemberCount: len(cfg.Members),
		}
		for _, m := range cfg.Members {
			ti.Members = append(ti.Members, memberInfo{
				Name:      m.Name,
				AgentType: m.AgentType,
				Model:     m.Model,
				Color:     m.Color,
			})
		}
		result = append(result, ti)
	}
	writeJSON(w, result)
}

func (s *Server) handleTeamDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, fmt.Errorf("missing team name"), http.StatusBadRequest)
		return
	}

	cfg, err := cc.ReadTeamConfig(name)
	if err != nil {
		writeError(w, fmt.Errorf("team %q: %w", name, err), http.StatusNotFound)
		return
	}

	// Read last N messages from each inbox.
	inboxes := map[string][]cc.InboxMessage{}
	for _, m := range cfg.Members {
		msgs, err := cc.ReadInbox(name, m.Name)
		if err != nil {
			continue
		}
		// Keep last 20 messages.
		if len(msgs) > 20 {
			msgs = msgs[len(msgs)-20:]
		}
		inboxes[m.Name] = msgs
	}

	// Find sessions belonging to this team.
	sessions, err := s.svc.Search(r.Context(), cass.SearchRequest{
		Filters: cass.Filters{Team: name},
		Limit:   50,
	})
	if err != nil {
		sessions = &cass.SearchResult{}
	}

	writeJSON(w, map[string]any{
		"config":   cfg,
		"inboxes":  inboxes,
		"sessions": sessions,
	})
}

func (s *Server) handleTeamInbox(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	agent := r.PathValue("agent")
	if name == "" || agent == "" {
		writeError(w, fmt.Errorf("missing team or agent name"), http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodPost {
		var body struct {
			From string `json:"from"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, fmt.Errorf("parse body: %w", err), http.StatusBadRequest)
			return
		}
		if body.From == "" {
			body.From = "web"
		}
		msg := cc.InboxMessage{
			From: body.From,
			Text: body.Text,
		}
		if err := cc.AppendInbox(name, agent, msg); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		s.broker.Publish(Event{
			Type: "team_message",
			Data: map[string]any{
				"team":  name,
				"agent": agent,
				"from":  body.From,
			},
		})
		writeJSON(w, map[string]string{"status": "sent"})
		return
	}

	msgs, err := cc.ReadInbox(name, agent)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, msgs)
}

// handleSessionRequests returns HAR-derived API requests for a session.
// GET /api/session/{id}/requests
func (s *Server) handleSessionRequests(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, fmt.Errorf("missing session id"), http.StatusBadRequest)
		return
	}

	requests, err := s.svc.QueryRequests(r.Context(), id)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if requests == nil {
		requests = []cass.APIRequest{}
	}
	writeJSON(w, requests)
}

// handleLimits returns rate-limit utilization trend data and HAR request counts.
// GET /api/limits?since=168h&bucket=5h
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	since := 168 * time.Hour
	if v := q.Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			since = d
		}
	}
	after := time.Now().Add(-since)

	bucketParam := q.Get("bucket") // "" means all three

	type bucketTrend struct {
		Bucket    string                  `json:"bucket"`
		Snapshots []cass.RateLimitSnapshot `json:"snapshots"`
	}

	var trends []bucketTrend
	for _, bucket := range []string{"5h", "7d", "7d_sonnet"} {
		if bucketParam != "" && bucketParam != bucket {
			continue
		}
		snaps, err := s.svc.RateLimitTrend(r.Context(), bucket, after)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		if snaps == nil {
			snaps = []cass.RateLimitSnapshot{}
		}
		trends = append(trends, bucketTrend{Bucket: bucket, Snapshots: snaps})
	}

	reqCount, _ := s.svc.APIRequestCount(r.Context())

	writeJSON(w, map[string]any{
		"trends":            trends,
		"api_request_count": reqCount,
	})
}

// handleUsage returns daily token usage from api_requests for the usage tab.
// GET /api/usage?since=168h
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	since := 720 * time.Hour // default 30d
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			since = d
		}
	}
	var after time.Time
	if since > 0 {
		after = time.Now().Add(-since)
	}

	daily, err := s.svc.DailyTokenUsage(r.Context(), after)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}

	// Also fetch latest rate-limit snapshots (most recent per bucket).
	type bucketLatest struct {
		Bucket      string  `json:"bucket"`
		Utilization float64 `json:"utilization"`
		ResetAt     int64   `json:"reset_at"`
		Timestamp   int64   `json:"timestamp"`
	}
	var latestSnaps []bucketLatest
	for _, bucket := range []string{"5h", "7d", "7d_sonnet"} {
		snaps, err := s.svc.RateLimitTrend(r.Context(), bucket, time.Now().Add(-24*time.Hour))
		if err != nil || len(snaps) == 0 {
			continue
		}
		latest := snaps[len(snaps)-1]
		var util float64
		var resetAt int64
		switch bucket {
		case "5h":
			util = latest.Utilization5h
			resetAt = latest.Reset5h
		case "7d":
			util = latest.Utilization7d
			resetAt = latest.Reset7d
		default:
			util = latest.ModelUtilization
			resetAt = latest.ModelReset
		}
		latestSnaps = append(latestSnaps, bucketLatest{
			Bucket:      bucket,
			Utilization: util,
			ResetAt:     resetAt,
			Timestamp:   latest.Timestamp,
		})
	}

	reqCount, _ := s.svc.APIRequestCount(r.Context())

	writeJSON(w, map[string]any{
		"daily":             daily,
		"latest_limits":     latestSnaps,
		"api_request_count": reqCount,
	})
}

// Ensure context is used (for future middleware).
var _ = context.Background
