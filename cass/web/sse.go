package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/tmc/cc/cass/service"
	"github.com/tmc/cc/ccpaths"
)

// Event is a server-sent event.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// SSEBroker manages SSE client connections and broadcasts events.
type SSEBroker struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
	closed  bool
}

// NewSSEBroker creates a new broker.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan Event]struct{}),
	}
}

// Subscribe registers a new client and returns its event channel. If the broker
// is already shut down, the returned channel is closed so the caller's read
// loop exits immediately.
func (b *SSEBroker) Subscribe() chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(ch)
		return ch
	}
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a client.
func (b *SSEBroker) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.clients[ch]; !ok {
		b.mu.Unlock()
		return
	}
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

// Shutdown closes every client channel so their SSE read loops exit, and marks
// the broker closed so later Subscribe/Publish calls are no-ops. Idempotent.
func (b *SSEBroker) Shutdown() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	chans := make([]chan Event, 0, len(b.clients))
	for ch := range b.clients {
		chans = append(chans, ch)
	}
	b.mu.Unlock()
	for _, ch := range chans {
		b.Unsubscribe(ch) // single close site, idempotent
	}
}

// Publish sends an event to all connected clients.
func (b *SSEBroker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for ch := range b.clients {
		select {
		case ch <- e:
		default:
			// Disconnect slow clients instead of silently dropping events.
			delete(b.clients, ch)
			close(ch)
		}
	}
}

// ServeHTTP handles SSE connections.
func (b *SSEBroker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	// Send initial keepalive.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
			flusher.Flush()
		}
	}
}

// ReindexFunc is called by the file watcher to incrementally re-index changed files.
type ReindexFunc func(ctx context.Context, paths []string) (int, error)

// FileWatcher watches session files and publishes SSE events on changes.
// When a ReindexFunc is set, it re-indexes changed files before publishing.
type FileWatcher struct {
	broker  *SSEBroker
	log     *slog.Logger
	w       *fsnotify.Watcher
	reindex ReindexFunc

	// Single-flight reindex state. Only one processPending runs at a time;
	// files arriving while one is in flight are merged into queued and drained
	// in a trailing run, so a constantly-written session (e.g. an active
	// Workflow appending agent files every few seconds) cannot stack dozens of
	// overlapping whole-session reparses across cores.
	mu      sync.Mutex
	running bool
	queued  map[string]struct{}
}

// NewFileWatcher creates a watcher for Claude Code session files.
func NewFileWatcher(broker *SSEBroker, log *slog.Logger, reindex ReindexFunc) (*FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}
	return &FileWatcher{
		broker:  broker,
		log:     log,
		w:       w,
		reindex: reindex,
	}, nil
}

// Start begins watching known session roots for file changes.
func (fw *FileWatcher) Start(ctx context.Context) {
	defer fw.w.Close()

	// Watch the projects directory.
	ch, err := ccpaths.ClaudeHome()
	if err != nil {
		fw.log.Error("home dir", "err", err)
		return
	}
	root := filepath.Join(ch, "projects")
	if err := fw.addDirRecursive(root); err != nil {
		fw.log.Warn("watch dir", "path", root, "err", err)
	}

	gh, err := ccpaths.GeminiHome()
	if err == nil && gh != "" {
		geminiRoot := filepath.Join(gh, "projects")
		if err := fw.addDirRecursive(geminiRoot); err != nil {
			fw.log.Warn("watch dir", "path", geminiRoot, "err", err)
		}
	}

	xh, err := ccpaths.CodexHome()
	if err == nil && xh != "" {
		codexRoot := filepath.Join(xh, "sessions")
		if err := fw.addDirRecursive(codexRoot); err != nil {
			fw.log.Warn("watch dir", "path", codexRoot, "err", err)
		}
	}

	oh, err := ccpaths.OpenCodeHome()
	if err == nil && oh != "" {
		opencodeRoot := filepath.Join(oh, "storage")
		if err := fw.addDirRecursive(opencodeRoot); err != nil {
			fw.log.Warn("watch dir", "path", opencodeRoot, "err", err)
		}
	}

	ph, err := ccpaths.PiHome()
	if err == nil && ph != "" {
		piRoot := filepath.Join(ph, "sessions")
		if err := fw.addDirRecursive(piRoot); err != nil {
			fw.log.Warn("watch dir", "path", piRoot, "err", err)
		}
	}

	// Debounce timer. Keep all access to pending on this goroutine; the timer
	// only signals through debounceC.
	var debounce *time.Timer
	var debounceC <-chan time.Time
	pending := make(map[string]struct{})
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fw.w.Events:
			if !ok {
				return
			}
			if !isSessionWatchFile(event.Name) {
				// Watch new directories (including subagent dirs).
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						fw.w.Add(event.Name)
					}
				}
				continue
			}
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
				continue
			}

			pending[event.Name] = struct{}{}
			if debounce == nil {
				debounce = time.NewTimer(500 * time.Millisecond)
				debounceC = debounce.C
			} else {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(500 * time.Millisecond)
			}

		case err, ok := <-fw.w.Errors:
			if !ok {
				return
			}
			fw.log.Warn("watcher error", "err", err)
		case <-debounceC:
			files := pending
			pending = make(map[string]struct{})
			debounce.Stop()
			debounce = nil
			debounceC = nil
			fw.schedule(ctx, files)
		}
	}
}

// schedule runs processPending under a single-flight guard. If a run is
// already in flight, the files are merged into the queued set and drained by
// a trailing run when the current one finishes, so reindex parallelism is
// capped at one regardless of how fast files change.
func (fw *FileWatcher) schedule(ctx context.Context, files map[string]struct{}) {
	fw.mu.Lock()
	if fw.running {
		if fw.queued == nil {
			fw.queued = make(map[string]struct{})
		}
		for f := range files {
			fw.queued[f] = struct{}{}
		}
		fw.mu.Unlock()
		return
	}
	fw.running = true
	fw.mu.Unlock()

	go func() {
		batch := files
		for {
			fw.processPending(ctx, batch)
			fw.mu.Lock()
			// Stop on cancellation before consuming the queue, so a merged batch
			// is never pulled-and-dropped; any queued files are left intact for
			// the next Start to reconcile.
			if ctx.Err() != nil || len(fw.queued) == 0 {
				fw.running = false
				fw.mu.Unlock()
				return
			}
			batch = fw.queued
			fw.queued = nil
			fw.mu.Unlock()
		}
	}()
}

// parentSessionPath maps a subagent JSONL path back to its parent session JSONL.
// e.g. /path/to/<uuid>/subagents/agent-xyz.jsonl → /path/to/<uuid>.jsonl
// For parent session files, returns the path unchanged. Delegates to
// [service.ParentSessionPath], the single source of truth shared with
// IndexPaths so the watcher and the indexer collapse subagent paths identically.
func parentSessionPath(p string) string {
	if q := opencodeParentSessionPath(p); q != "" {
		return q
	}
	return service.ParentSessionPath(p)
}

func isSessionWatchFile(path string) bool {
	if strings.HasSuffix(path, ".jsonl") {
		return true
	}
	p := filepath.ToSlash(path)
	return strings.Contains(p, "/storage/session/") && strings.HasPrefix(filepath.Base(path), "ses_") && strings.HasSuffix(path, ".json") ||
		strings.Contains(p, "/storage/message/") && strings.HasSuffix(path, ".json") ||
		strings.Contains(p, "/storage/part/") && strings.HasSuffix(path, ".json")
}

func opencodeParentSessionPath(path string) string {
	p := filepath.ToSlash(path)
	switch {
	case strings.Contains(p, "/storage/session/"):
		return path
	case strings.Contains(p, "/storage/message/"):
		sid := filepath.Base(filepath.Dir(path))
		return findOpenCodeSessionPath(path, sid)
	case strings.Contains(p, "/storage/part/"):
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		var part struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(data, &part) != nil || part.SessionID == "" {
			return ""
		}
		return findOpenCodeSessionPath(path, part.SessionID)
	default:
		return ""
	}
}

func findOpenCodeSessionPath(path, sid string) string {
	root := opencodeStorageRoot(path)
	if root == "" || sid == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(root, "session", "*", sid+".json"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func opencodeStorageRoot(path string) string {
	dir := filepath.Dir(path)
	for filepath.Base(dir) != "storage" {
		next := filepath.Dir(dir)
		if next == dir {
			return ""
		}
		dir = next
	}
	return dir
}

func (fw *FileWatcher) processPending(ctx context.Context, files map[string]struct{}) {
	if len(files) == 0 {
		return
	}

	// Deduplicate: map subagent files to parent session paths for reindexing.
	parentPaths := make(map[string]struct{})
	for f := range files {
		parentPaths[parentSessionPath(f)] = struct{}{}
	}

	var paths []string
	for p := range parentPaths {
		paths = append(paths, p)
	}

	// Incrementally re-index the changed files so the UI sees fresh data.
	// Retry on DB contention with backoff.
	indexed := 0
	if fw.reindex != nil {
		for attempt := range 3 {
			n, err := fw.reindex(ctx, paths)
			if err != nil {
				if strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "SQLITE_BUSY") {
					fw.log.Debug("incremental reindex waiting", "attempt", attempt+1, "err", err)
					time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
					continue
				}
				fw.log.Warn("incremental reindex", "err", err)
			} else {
				indexed = n
			}
			break
		}
	}

	// Collect changed file basenames so the UI can match open sessions.
	var changedFiles []string
	for f := range files {
		changedFiles = append(changedFiles, f)
	}

	fw.log.Info("session files changed", "count", len(files), "indexed", indexed)

	fw.broker.Publish(Event{
		Type: "session_change",
		Data: map[string]any{
			"files_changed": len(files),
			"indexed":       indexed,
			"paths":         changedFiles,
			"timestamp":     time.Now().Format(time.RFC3339),
		},
	})
}

func (fw *FileWatcher) addDirRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return fw.w.Add(path)
		}
		return nil
	})
}
