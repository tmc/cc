package collector

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestCursorNameAndDetect(t *testing.T) {
	c := &Cursor{Root: filepath.Join(t.TempDir(), "missing")}
	if got := c.Name(); got != "cursor" {
		t.Errorf("Name = %q, want cursor", got)
	}
	res, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect missing root: %v", err)
	}
	if res.Found {
		t.Errorf("Detect missing root: Found = true, want false")
	}

	root := t.TempDir()
	c.Root = root
	res, err = c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect existing root: %v", err)
	}
	if !res.Found || len(res.Paths) != 1 || res.Paths[0] != root {
		t.Errorf("Detect existing root = %+v", res)
	}
}

func TestCursorScanStateDB(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "workspace-1")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	workspaceURI := url.URL{Scheme: "file", Path: workspace}
	writeFile(t, filepath.Join(storage, "workspace.json"), `{"folder":`+quoteJSON(workspaceURI.String())+`}`)

	dbPath := filepath.Join(storage, "state.vscdb")
	db := openCursorTestDB(t, dbPath)
	defer db.Close()
	payload := `{
		"allComposers": [{
			"composerId": "composer-1",
			"name": "Implement Cursor collector",
			"conversation": [
				{"id":"m1","role":"user","text":"Add Cursor support","createdAt":"2026-04-01T10:00:00Z"},
				{"id":"m2","role":"assistant","content":[{"text":"Done."}],"createdAt":"2026-04-01T10:00:15Z"}
			]
		}],
		"settings": {"messages": [{"role":"notice","text":"not a chat"}]}
	}`
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "cursor.composerData", payload); err != nil {
		t.Fatal(err)
	}

	c := &Cursor{Root: root}
	out := make(chan cass.Session, 2)
	if err := c.Scan(context.Background(), cass.ScanConfig{}, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	if len(got) != 1 {
		t.Fatalf("Scan returned %d sessions, want 1", len(got))
	}

	s := got[0]
	if !strings.HasPrefix(s.ID, "cur-") {
		t.Errorf("ID = %q, want cur-*", s.ID)
	}
	if s.Agent != "cursor" {
		t.Errorf("Agent = %q, want cursor", s.Agent)
	}
	if s.Title != "Implement Cursor collector" {
		t.Errorf("Title = %q, want Implement Cursor collector", s.Title)
	}
	if s.Workspace != workspace {
		t.Errorf("Workspace = %q, want %q", s.Workspace, workspace)
	}
	if s.SourcePath != dbPath {
		t.Errorf("SourcePath = %q, want %q", s.SourcePath, dbPath)
	}
	if len(s.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(s.Messages))
	}
	if s.Messages[0].Role != "user" || s.Messages[0].Content != "Add Cursor support" {
		t.Errorf("first message = %+v", s.Messages[0])
	}
	if s.Messages[1].Role != "assistant" || s.Messages[1].Content != "Done." {
		t.Errorf("second message = %+v", s.Messages[1])
	}
	if s.Stats.Turns != 1 {
		t.Errorf("Stats.Turns = %d, want 1", s.Stats.Turns)
	}
	if s.Stats.DurationSecs != 15 {
		t.Errorf("Stats.DurationSecs = %d, want 15", s.Stats.DurationSecs)
	}
	if s.Metadata["cursor_session_id"] != "composer-1" {
		t.Errorf("Metadata.cursor_session_id = %v, want composer-1", s.Metadata["cursor_session_id"])
	}
	if s.Metadata["storage_table"] != "ItemTable" {
		t.Errorf("Metadata.storage_table = %v, want ItemTable", s.Metadata["storage_table"])
	}
}

func TestCursorScanSinceFilter(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "workspace-1")
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	db := openCursorTestDB(t, filepath.Join(storage, "state.vscdb"))
	defer db.Close()
	payload := `{"conversation":[{"role":"user","text":"old","createdAt":"2020-01-01T00:00:00Z"}]}`
	if _, err := db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "cursor.old", payload); err != nil {
		t.Fatal(err)
	}

	c := &Cursor{Root: root}
	out := make(chan cass.Session, 1)
	cfg := cass.ScanConfig{Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := c.Scan(context.Background(), cfg, out); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	if len(got) != 0 {
		t.Errorf("Scan with Since returned %d sessions, want 0", len(got))
	}
}

func openCursorTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable(key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}
