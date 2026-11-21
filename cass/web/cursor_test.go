package web

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/collector"
	"github.com/tmc/cc/cass/service"
)

func TestCursorSessionSnapshot(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable VALUES (?, ?)`, "cursor.composerData", `{"allComposers":[
		{"composerId":"first","conversation":[{"id":"only-first","role":"user","text":"first fixture"}]},
		{"composerId":"second","conversation":[{"id":"only-second","role":"user","text":"second fixture"}]}
	]}`); err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(service.Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{&collector.Cursor{Root: root}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx := t.Context()
	if n, err := svc.Index(ctx, true); err != nil || n != 2 {
		t.Fatalf("Index = %d, %v, want 2 sessions", n, err)
	}
	res, err := svc.Search(ctx, cass.SearchRequest{Query: "first"})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("Search = %+v, %v, want the first session", res, err)
	}
	path := "/api/session/" + res.Hits[0].SessionID
	handler := New(Config{Service: svc}).Handler()
	for _, tt := range []struct{ name, accept, query, done string }{
		{"all", "application/json", "", ""},
		{"recent", "application/json", "?order=desc&limit=1", ""},
		{"stream", "text/event-stream", "?follow=true", "event: done"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path+tt.query, nil)
			request.Header.Set("Accept", tt.accept)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, request)
			body := w.Body.String()
			if w.Code != http.StatusOK || !strings.Contains(body, "first fixture") || strings.Contains(body, "second fixture") || !strings.Contains(body, tt.done) {
				t.Fatalf("snapshot = %d %s", w.Code, body)
			}
		})
	}
}
