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
}

// NewSSEBroker creates a new broker.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		clients: make(map[chan Event]struct{}),
	}
}

// Subscribe registers a new client and returns its event channel.
func (b *SSEBroker) Subscribe() chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a client.
func (b *SSEBroker) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

// Publish sends an event to all connected clients.
func (b *SSEBroker) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- e:
		default:
			// Drop event if client is slow.
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

// FileWatcher watches session files and publishes SSE events on changes.
type FileWatcher struct {
	svc    *service.Service
	broker *SSEBroker
	log    *slog.Logger
	w      *fsnotify.Watcher
}

// NewFileWatcher creates a watcher for Claude Code session files.
func NewFileWatcher(svc *service.Service, broker *SSEBroker, log *slog.Logger) (*FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create watcher: %w", err)
	}
	return &FileWatcher{
		svc:    svc,
		broker: broker,
		log:    log,
		w:      w,
	}, nil
}

// Start begins watching ~/.claude/projects/ for session file changes.
func (fw *FileWatcher) Start(ctx context.Context) {
	defer fw.w.Close()

	// Watch the projects directory.
	home, err := os.UserHomeDir()
	if err != nil {
		fw.log.Error("home dir", "err", err)
		return
	}
	root := filepath.Join(home, ".claude", "projects")
	if err := fw.addDirRecursive(root); err != nil {
		fw.log.Warn("watch dir", "path", root, "err", err)
	}

	// Debounce timer.
	var debounce *time.Timer
	pending := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fw.w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(event.Name, ".jsonl") {
				// Watch new directories.
				if event.Has(fsnotify.Create) {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						if filepath.Base(event.Name) != "subagents" {
							fw.w.Add(event.Name)
						}
					}
				}
				continue
			}
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) {
				continue
			}

			pending[event.Name] = struct{}{}
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				fw.processPending(ctx, pending)
				pending = make(map[string]struct{})
			})

		case err, ok := <-fw.w.Errors:
			if !ok {
				return
			}
			fw.log.Warn("watcher error", "err", err)
		}
	}
}

func (fw *FileWatcher) processPending(ctx context.Context, files map[string]struct{}) {
	if len(files) == 0 {
		return
	}

	// Re-index to pick up changes.
	count, err := fw.svc.Index(ctx, false)
	if err != nil {
		fw.log.Error("reindex on change", "err", err)
		return
	}

	fw.broker.Publish(Event{
		Type: "session_change",
		Data: map[string]any{
			"files_changed": len(files),
			"indexed":       count,
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
			if info.Name() == "subagents" {
				return filepath.SkipDir
			}
			return fw.w.Add(path)
		}
		return nil
	})
}
