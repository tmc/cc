package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/ccinboxstore"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/ccteamcfg"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := cass.SearchRequest{
		Query: q.Get("q"),
		Filters: cass.Filters{
			Agent:      q.Get("agent"),
			Workspace:  q.Get("workspace"),
			Team:       q.Get("team"),
			GoalStatus: q.Get("goal_status"),
			Skill:      q.Get("skill"),
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
	if q.Get("count") == "false" {
		req.SkipCount = true
	}
	if v := q.Get("sort"); v != "" {
		req.Sort = cass.SortMode(v)
	}
	switch cass.ChildMode(q.Get("children")) {
	case cass.ChildrenExpanded:
		req.Children = cass.ChildrenExpanded
	case cass.ChildrenRaw:
		req.Children = cass.ChildrenRaw
	default:
		req.Children = cass.ChildrenCollapsed
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

	summary := q.Get("summary") == "true"
	var result *cass.SearchResult
	var err error
	if summary {
		result, err = s.svc.SearchSummary(r.Context(), req)
	} else {
		result, err = s.svc.Search(r.Context(), req)
	}
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	if summary {
		for i := range result.Hits {
			trimSearchHit(&result.Hits[i])
		}
	}
	writeJSON(w, result)
}

func trimSearchHit(h *cass.Hit) {
	if h == nil {
		return
	}
	h.Goals = nil
	h.Skills = trimSearchSkills(h.Skills)
	h.Workflows = nil
	h.SummaryOnly = true
}

func trimSearchSkills(skills []cass.SkillUse) []cass.SkillUse {
	var out []cass.SkillUse
	seen := map[string]bool{}
	for _, skill := range skills {
		switch skill.Kind {
		case "selected", "tool", "loaded":
		default:
			continue
		}
		if skill.Name == "" {
			continue
		}
		key := skill.Kind + "\x00" + skill.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, cass.SkillUse{
			Name:  skill.Name,
			Kind:  skill.Kind,
			Count: skill.Count,
		})
	}
	return out
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

func (s *Server) handleGoals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	goals, err := s.svc.Goals(r.Context(), q.Get("status"), limit)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, goals)
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	skills, err := s.svc.Skills(r.Context(), q.Get("skill"), q.Get("kind"), limit)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, skills)
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

	// Non-fatal: team configs may not exist.
	teamConfigCount, _ := s.svc.IndexTeamConfigs(r.Context(), "")

	elapsed := time.Since(start)

	result := map[string]any{
		"indexed":           count,
		"artifact_requests": artifactCount,
		"team_configs":      teamConfigCount,
		"duration_ms":       elapsed.Milliseconds(),
	}

	// Publish SSE event.
	s.broker.Publish(Event{
		Type: "index_complete",
		Data: result,
	})

	writeJSON(w, result)
}

func (s *Server) handleSessionMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, fmt.Errorf("missing session id"), http.StatusBadRequest)
		return
	}
	hit, err := s.svc.Session(r.Context(), id)
	if err != nil {
		writeError(w, fmt.Errorf("session not found: %s", id), http.StatusNotFound)
		return
	}
	writeJSON(w, hit)
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
	sourcePath, err := s.svc.SourcePath(r.Context(), id)
	if err != nil || sourcePath == "" {
		writeError(w, fmt.Errorf("session not found: %s", id), http.StatusNotFound)
		return
	}

	// Check if client wants SSE streaming or JSON dump.
	if r.Header.Get("Accept") == "text/event-stream" {
		s.streamSession(w, r, sourcePath)
		return
	}

	// Default: return all entries as JSON array (including subagent entries).
	entries, err := readSessionEntriesWithSubagents(sourcePath)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, pageSessionEntries(entries, r))
}

// handleWorkflowAgent serves a single workflow fan-out agent transcript by its
// source_path. Workflow agents are not sessions rows (they fold into their
// parent), so they cannot be fetched via /api/session/{id}. The path is taken
// from the client, so it is strictly validated — symlink-resolved and confined
// to a subagents/workflows/ transcript under the Claude projects root — to
// prevent reading arbitrary files.
func (s *Server) handleWorkflowAgent(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("source_path")
	if raw == "" {
		writeError(w, fmt.Errorf("missing source_path"), http.StatusBadRequest)
		return
	}
	path, err := validateWorkflowAgentPath(raw)
	if err != nil {
		writeError(w, err, http.StatusForbidden)
		return
	}
	if r.Header.Get("Accept") == "text/event-stream" {
		s.streamSession(w, r, path)
		return
	}
	// Single agent file: no subagent merge (an agent has no subagents/ of its own).
	entries, err := readSessionEntries(path)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, pageSessionEntries(entries, r))
}

// validateWorkflowAgentPath confirms p is a workflow-agent transcript safely
// reachable: it must be an agent-*.jsonl under a subagents/workflows/ segment,
// and (symlink-resolved) reside under the Claude projects root. Returns the
// resolved path to open.
func validateWorkflowAgentPath(p string) (string, error) {
	if !strings.HasSuffix(p, ".jsonl") || !strings.HasPrefix(filepath.Base(p), "agent-") {
		return "", fmt.Errorf("not a workflow-agent transcript")
	}
	if !strings.Contains(filepath.ToSlash(p), "/subagents/workflows/") {
		return "", fmt.Errorf("not under subagents/workflows/")
	}
	home, err := ccpaths.ClaudeHome()
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(filepath.Join(home, "projects"))
	if err != nil {
		return "", err
	}
	// Resolve symlinks on the target before the prefix check so "../" and
	// symlink escapes cannot leave the projects root.
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes projects root")
	}
	return resolved, nil
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

	follow := r.URL.Query().Get("follow") == "true"

	entries, err := readSessionEntriesWithSubagents(path)
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

	if !follow {
		fmt.Fprintf(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
		return
	}

	// Signal the client that initial catch-up is complete; now tailing.
	fmt.Fprintf(w, "event: caught_up\ndata: {\"count\":%d}\n\n", len(entries))
	flusher.Flush()

	s.tailSession(ctx, w, flusher, path, len(entries))
}

// tailSession watches the session's JSONL files for new entries and streams them.
// It monitors the parent file and the subagents directory.
func (s *Server) tailSession(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, path string, sentCount int) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		fmt.Fprintf(w, "event: error\ndata: %q\n\n", "failed to create watcher")
		flusher.Flush()
		return
	}
	defer watcher.Close()

	// Watch the parent JSONL file.
	if err := watcher.Add(path); err != nil {
		s.log.Debug("tail: cannot watch parent", "path", path, "err", err)
	}

	// Watch the session directory (e.g. <uuid>/) to detect subagents/ creation.
	sessionDir := strings.TrimSuffix(path, ".jsonl")
	if info, err := os.Stat(sessionDir); err == nil && info.IsDir() {
		watcher.Add(sessionDir)
	} else {
		// Session directory doesn't exist yet — watch the parent projects directory
		// so we can detect when it gets created.
		watcher.Add(filepath.Dir(path))
	}

	// Watch the subagents directory if it already exists.
	subagentDir := filepath.Join(sessionDir, "subagents")
	if info, err := os.Stat(subagentDir); err == nil && info.IsDir() {
		watcher.Add(subagentDir)
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	idle := time.NewTimer(30 * time.Minute)
	defer idle.Stop()

	// Debounce channel: the debounce timer fires a signal here.
	debounceCh := make(chan struct{}, 1)
	var debounce *time.Timer

	// Track seen UUIDs to avoid sending duplicates.
	// The merged entry list is sorted by timestamp, so new entries from
	// subagents can appear anywhere in the sorted order — we can't just
	// slice from sentCount.
	seenUUIDs := make(map[string]bool, sentCount)
	initialEntries, _ := readSessionEntriesWithSubagents(path)
	for _, e := range initialEntries {
		if e.UUID != "" {
			seenUUIDs[e.UUID] = true
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-idle.C:
			fmt.Fprintf(w, "event: done\ndata: {\"reason\":\"idle_timeout\"}\n\n")
			flusher.Flush()
			return

		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()

		case <-debounceCh:
			allEntries, err := readSessionEntriesWithSubagents(path)
			if err != nil {
				continue
			}
			if len(allEntries) <= sentCount {
				continue
			}

			sent := 0
			for _, entry := range allEntries {
				if entry.UUID != "" && seenUUIDs[entry.UUID] {
					continue
				}
				if entry.UUID != "" {
					seenUUIDs[entry.UUID] = true
				}
				data, err := json.Marshal(entry)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "event: entry\ndata: %s\n\n", data)
				sent++
			}
			if sent > 0 {
				flusher.Flush()
				sentCount = len(allEntries)
				idle.Reset(30 * time.Minute)
			}

		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) {
				continue
			}

			// If a new directory appeared, start watching it.
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					watcher.Add(ev.Name)
					// Don't continue — there may also be .jsonl files to process.
				}
			}

			if !strings.HasSuffix(ev.Name, ".jsonl") {
				continue
			}

			// Debounce rapid writes (100ms).
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(100*time.Millisecond, func() {
				select {
				case debounceCh <- struct{}{}:
				default:
				}
			})

		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func readSessionEntries(path string) ([]cc.Entry, error) {
	// Gemini CLI chat files are JSON objects, not JSONL.
	if strings.HasSuffix(path, ".json") && strings.HasPrefix(filepath.Base(path), "session-") {
		return readGeminiJSONSessionEntries(path)
	}
	return cc.ReadFile(context.Background(), path)
}

func readGeminiJSONSessionEntries(path string) ([]cc.Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sess struct {
		SessionID string `json:"sessionId"`
		Messages  []struct {
			ID        string          `json:"id"`
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Content   json.RawMessage `json:"content"`
			Model     string          `json:"model"`
			Tokens    struct {
				Input  int `json:"input"`
				Output int `json:"output"`
				Cached int `json:"cached"`
			} `json:"tokens"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, err
	}

	entries := make([]cc.Entry, 0, len(sess.Messages))
	for _, m := range sess.Messages {
		if len(m.Content) == 0 {
			continue
		}
		if cc.ExtractAnyText(m.Content) == "" {
			continue
		}
		role := "assistant"
		if m.Type == "user" {
			role = "user"
		}
		ts, _ := time.Parse(time.RFC3339, m.Timestamp)
		e := cc.Entry{
			Type:      "message",
			SessionID: sess.SessionID,
			UUID:      m.ID,
			Timestamp: ts,
			Message: &cc.Message{
				ID:      m.ID,
				Role:    role,
				Content: m.Content,
				Model:   m.Model,
			},
		}
		if role == "assistant" {
			e.Message.Usage = &cc.Usage{
				InputTokens:          m.Tokens.Input,
				OutputTokens:         m.Tokens.Output,
				CacheReadInputTokens: m.Tokens.Cached,
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// readSessionEntriesWithSubagents reads the parent session JSONL and merges
// entries from any subagent files found at <session-dir>/subagents/agent-*.jsonl.
// Subagent entries are tagged with AgentID and IsSidechain=true.
// The merged result is sorted by timestamp.
func readSessionEntriesWithSubagents(path string) ([]cc.Entry, error) {
	entries, err := readSessionEntries(path)
	if err != nil {
		return nil, err
	}

	subagentDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	infos, err := os.ReadDir(subagentDir)
	if err != nil {
		// No subagents directory — return parent entries only.
		return entries, nil
	}
	for _, fi := range infos {
		name := fi.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-acompact") {
			continue
		}
		sub, err := readSessionEntries(filepath.Join(subagentDir, name))
		if err != nil {
			continue
		}
		// Extract agent ID from filename: agent-<id>.jsonl
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		for i := range sub {
			if sub[i].AgentID == "" {
				sub[i].AgentID = agentID
			}
			sub[i].IsSidechain = true
		}
		entries = append(entries, sub...)
	}

	// Workflow fan-out agents nest a level deeper, under
	// subagents/workflows/<run_id>/agent-*.jsonl. Include them as sidechains so
	// the parent's flat transcript shows their activity too.
	wfAgents, _ := filepath.Glob(filepath.Join(subagentDir, "workflows", "*", "agent-*.jsonl"))
	for _, wfPath := range wfAgents {
		base := filepath.Base(wfPath)
		if strings.HasPrefix(base, "agent-acompact") {
			continue
		}
		sub, err := readSessionEntries(wfPath)
		if err != nil {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(base, "agent-"), ".jsonl")
		for i := range sub {
			if sub[i].AgentID == "" {
				sub[i].AgentID = agentID
			}
			sub[i].IsSidechain = true
		}
		entries = append(entries, sub...)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

func pageSessionEntries(entries []cc.Entry, r *http.Request) []cc.Entry {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if limit <= 0 && offset <= 0 && q.Get("order") != "desc" {
		return entries
	}

	out := entries
	if q.Get("order") == "desc" {
		out = append([]cc.Entry(nil), entries...)
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(out) {
		return []cc.Entry{}
	}
	out = out[offset:]
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

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

	// Workflow collapsing mode; default is collapsed.
	opts := cass.GraphOptions{Workflow: cass.WorkflowCollapsed}
	switch cass.WorkflowMode(q.Get("workflow")) {
	case cass.WorkflowExpanded:
		opts.Workflow = cass.WorkflowExpanded
	case cass.WorkflowNone:
		opts.Workflow = cass.WorkflowNone
	case cass.WorkflowCollapsed:
		opts.Workflow = cass.WorkflowCollapsed
	}
	// Optional node_type=a,b,c filter.
	if v := q.Get("node_type"); v != "" {
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				opts.NodeTypes = append(opts.NodeTypes, t)
			}
		}
	}

	graph, err := s.svc.Graph(r.Context(), since, opts)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, graph)
}

func (s *Server) handleTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := ccteamcfg.ListTeams()
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
		cfg, err := ccteamcfg.ReadTeamConfig(name)
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

	cfg, err := ccteamcfg.ReadTeamConfig(name)
	if err != nil {
		writeError(w, fmt.Errorf("team %q: %w", name, err), http.StatusNotFound)
		return
	}

	// Read last N messages from each inbox.
	inboxes := map[string][]ccinboxstore.InboxMessage{}
	for _, m := range cfg.Members {
		msgs, err := ccinboxstore.ReadInbox(r.Context(), name, m.Name)
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
		msg := ccinboxstore.InboxMessage{
			From: body.From,
			Text: body.Text,
		}
		if err := ccinboxstore.AppendInbox(r.Context(), name, agent, msg); err != nil {
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

	msgs, err := ccinboxstore.ReadInbox(r.Context(), name, agent)
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
		Bucket    string                   `json:"bucket"`
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
