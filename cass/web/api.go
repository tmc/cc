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

	start := time.Now()
	count, err := s.svc.Index(r.Context(), force)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	elapsed := time.Since(start)

	result := map[string]any{
		"indexed":     count,
		"duration_ms": elapsed.Milliseconds(),
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

	// Look up the session's source path.
	result, err := s.svc.Search(r.Context(), cass.SearchRequest{
		Query: id,
		Limit: 1,
	})
	if err != nil || len(result.Hits) == 0 {
		writeError(w, fmt.Errorf("session not found: %s", id), http.StatusNotFound)
		return
	}

	sourcePath := result.Hits[0].SourcePath
	if sourcePath == "" {
		writeError(w, fmt.Errorf("no source path for session"), http.StatusNotFound)
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
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

// Ensure context is used (for future middleware).
var _ = context.Background
