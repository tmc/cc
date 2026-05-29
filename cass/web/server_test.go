package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSSEBrokerShutdown(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()

	b.Shutdown()

	// The subscribed channel must be closed so its read loop exits.
	if _, ok := <-ch; ok {
		t.Fatal("expected subscriber channel closed after Shutdown")
	}
	// Shutdown is idempotent.
	b.Shutdown()
	// Publish after shutdown is a no-op (must not panic).
	b.Publish(Event{Type: "noop"})
	// Subscribe after shutdown returns an already-closed channel.
	ch2 := b.Subscribe()
	if _, ok := <-ch2; ok {
		t.Fatal("expected closed channel from Subscribe after Shutdown")
	}
}

func TestPanicRecovery(t *testing.T) {
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.panicRecovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rr := httptest.NewRecorder()
	// Must not propagate the panic past the middleware.
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/search", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if rr.Body.Len() == 0 {
		t.Fatalf("expected an error body")
	}
}
