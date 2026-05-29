package main

// Example Go backend for streaming Claude Code session data via SSE
// This demonstrates how to serve JSONL session entries to the frontend

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// SessionEvent represents a single Claude Code session event
type SessionEvent struct {
	Type      string                 `json:"type"` // "user", "assistant", "tool_use", "tool_result"
	Timestamp time.Time              `json:"timestamp"`
	Content   string                 `json:"content"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// StreamSessionHandler serves session events via Server-Sent Events (SSE)
func StreamSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Get flusher for streaming
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Find session file
	sessionFile := fmt.Sprintf("sessions/%s.jsonl", sessionID)
	file, err := os.Open(sessionFile)
	if err != nil {
		http.Error(w, fmt.Sprintf("session not found: %v", err), http.StatusNotFound)
		return
	}
	defer file.Close()

	// Stream events from JSONL file
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()

		// Parse JSONL line
		var event SessionEvent
		if err := json.Unmarshal(line, &event); err != nil {
			log.Printf("failed to parse event: %v", err)
			continue
		}

		// Send as SSE event
		eventData, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", eventData)
		flusher.Flush()

		// Optional: Add small delay for realistic replay
		time.Sleep(100 * time.Millisecond)

		// Check if client disconnected
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("error reading session: %v", err)
	}
}

// GetSessionHandler returns session metadata
func GetSessionHandler(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionId")
	if sessionID == "" {
		http.Error(w, "session ID required", http.StatusBadRequest)
		return
	}

	// Return session info (simplified example)
	info := map[string]interface{}{
		"id":        sessionID,
		"createdAt": time.Now(),
		"status":    "completed",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// ListSessionsHandler returns list of available sessions
func ListSessionsHandler(w http.ResponseWriter, r *http.Request) {
	sessions := []map[string]interface{}{
		{
			"id":        "demo-session-1",
			"createdAt": time.Now().Add(-1 * time.Hour),
			"status":    "completed",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func main() {
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/sessions", ListSessionsHandler)
	mux.HandleFunc("GET /api/sessions/{sessionId}", GetSessionHandler)
	mux.HandleFunc("GET /api/sessions/{sessionId}/stream", StreamSessionHandler)

	// Serve static files (the HTML examples)
	fs := http.FileServer(http.Dir("."))
	mux.Handle("/", fs)

	addr := ":8080"
	log.Printf("Server listening on %s", addr)
	log.Printf("Try: http://localhost:8080/sse-integration-example.html")
	log.Fatal(http.ListenAndServe(addr, mux))
}

// Example usage:
//
// 1. Create a session JSONL file:
//    sessions/demo-session-1.jsonl
//
// 2. Populate with events:
//    {"type":"user","timestamp":"2026-02-14T10:00:00Z","content":"Help me debug this code"}
//    {"type":"assistant","timestamp":"2026-02-14T10:00:01Z","content":"I'll help you debug that"}
//    {"type":"tool_use","timestamp":"2026-02-14T10:00:02Z","content":"","metadata":{"name":"Read","input":{"file_path":"main.go"}}}
//    {"type":"tool_result","timestamp":"2026-02-14T10:00:03Z","content":"package main\n\nfunc main() {...}"}
//
// 3. Run the server:
//    go run go-sse-backend.go
//
// 4. Open browser:
//    http://localhost:8080/sse-integration-example.html
//
// 5. Connect with session ID:
//    demo-session-1
