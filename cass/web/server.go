package web

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/cass/service"
)

//go:embed static
var staticFS embed.FS

// Config configures the web server.
type Config struct {
	Service    *service.Service
	Addr       string // Listen address, default ":8080".
	DevMode    bool   // Serve static files from disk for development.
	DevStaticDir string // Override dev static directory path.
	Verbose    bool   // Log requests with timing info.
	Logger     *slog.Logger
}

// Server serves the CASS web UI and API.
type Server struct {
	svc     *service.Service
	broker  *SSEBroker
	addr    string
	dev     bool
	devDir  string
	verbose bool
	log     *slog.Logger
	srv     *http.Server
}

// New creates a new web server.
func New(cfg Config) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":8080"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Server{
		svc:     cfg.Service,
		broker:  NewSSEBroker(),
		addr:    cfg.Addr,
		dev:     cfg.DevMode,
		devDir:  cfg.DevStaticDir,
		verbose: cfg.Verbose,
		log:     cfg.Logger,
	}
}

// Handler returns the configured HTTP handler with all API routes and static
// file serving registered. Use this to embed the CASS UI inside another server:
//
//	mux.Handle("/cass/", http.StripPrefix("/cass", cassServer.Handler()))
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// API routes.
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/links", s.handleLinks)
	mux.HandleFunc("GET /api/mappings", s.handleMappings)
	mux.HandleFunc("GET /api/labels", s.handleLabels)
	mux.HandleFunc("POST /api/index", s.handleIndex)
	mux.HandleFunc("GET /api/session/{id}", s.handleSessionStream)
	mux.HandleFunc("GET /api/session/{id}/requests", s.handleSessionRequests)
	mux.HandleFunc("GET /api/limits", s.handleLimits)
	mux.HandleFunc("GET /api/graph", s.handleGraph)
	mux.HandleFunc("GET /api/teams", s.handleTeams)
	mux.HandleFunc("GET /api/teams/{name}", s.handleTeamDetail)
	mux.HandleFunc("GET /api/teams/{name}/inbox/{agent}", s.handleTeamInbox)
	mux.HandleFunc("POST /api/teams/{name}/inbox/{agent}", s.handleTeamInbox)

	// SSE endpoint.
	mux.HandleFunc("GET /events", s.broker.ServeHTTP)

	// Static files.
	mux.HandleFunc("/", s.serveStatic)

	if s.verbose {
		return s.requestLogger(mux)
	}
	return mux
}

// requestLogger wraps a handler with request timing logs.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"status", rw.status,
			"bytes", rw.bytes,
			"duration", time.Since(start).Round(time.Microsecond),
		)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Start begins serving HTTP and watching for file changes.
// It blocks until the context is cancelled or a signal is received.
func (s *Server) Start(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: s.Handler(),
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	// Start file watcher.
	watcher, err := NewFileWatcher(s.broker, s.log, s.svc.IndexPaths)
	if err != nil {
		s.log.Warn("file watcher unavailable", "err", err)
	} else {
		go watcher.Start(ctx)
	}

	// Start server.
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// Close shuts down the server.
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if s.dev {
		s.serveFromDisk(w, r)
		return
	}
	s.serveEmbedded(w, r)
}

func (s *Server) serveEmbedded(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	// Try to serve the file. If not found, serve index.html (SPA fallback).
	if _, err := fs.Stat(sub, path); err != nil {
		path = "index.html"
	}

	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, sub, path)
}

func (s *Server) serveFromDisk(w http.ResponseWriter, r *http.Request) {
	dir := s.devDir
	if dir == "" {
		// Find the static directory relative to the working directory.
		// Try common locations.
		for _, candidate := range []string{
			"cass/web/static",
			filepath.Join(os.Getenv("GOPATH"), "src/github.com/tmc/cc/cass/web/static"),
		} {
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				dir = candidate
				break
			}
		}
	}
	if dir == "" {
		http.Error(w, "dev static dir not found", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}

	fullPath := filepath.Join(dir, path)
	if _, err := os.Stat(fullPath); err != nil {
		fullPath = filepath.Join(dir, "index.html")
	}

	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, fullPath)
}

// addr returns the formatted listen URL.
func listenURL(addr string) string {
	host, port, _ := net.SplitHostPort(addr)
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}
