package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
