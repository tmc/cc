package cc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setEmptyHomes points every harness home at empty directories under root so
// resolution never touches the real session stores.
func setEmptyHomes(t *testing.T, root string) {
	t.Helper()
	t.Setenv("CLAUDE_HOME", root)
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini-empty"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex-empty"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode-empty"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi-empty"))
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, "antigravity-empty"))
}

func TestResolveSessionFile(t *testing.T) {
	root := t.TempDir()
	setEmptyHomes(t, root)

	dir := filepath.Join(root, "projects", "-repo-alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "c82a34fb-042b-48f2-8b00-023df3309812.jsonl")
	other := filepath.Join(dir, "c8000000-1111-2222-3333-444444444444.jsonl")
	line := `{"type":"user","timestamp":"2026-04-01T00:00:00Z"}` + "\n"
	for _, f := range []string{session, other} {
		if err := os.WriteFile(f, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	since := 24 * time.Hour

	// An existing path is returned as-is, without any lookup.
	if got, err := ResolveSessionFile(ctx, session, since); err != nil || got != session {
		t.Errorf("ResolveSessionFile(path) = %q, %v; want %q", got, err, session)
	}

	// A unique ID prefix resolves via the filename scan.
	if got, err := ResolveSessionFile(ctx, "c82a34fb", since); err != nil || got != session {
		t.Errorf("ResolveSessionFile(prefix) = %q, %v; want %q", got, err, session)
	}

	// A shared prefix is ambiguous and the error lists both matches.
	if _, err := ResolveSessionFile(ctx, "c8", since); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), session) ||
		!strings.Contains(err.Error(), other) {
		t.Errorf("ResolveSessionFile(ambiguous) err = %v; want ambiguous error listing both files", err)
	}

	// No match reports the search window.
	if _, err := ResolveSessionFile(ctx, "deadbeef", since); err == nil ||
		!strings.Contains(err.Error(), "no session matches") {
		t.Errorf("ResolveSessionFile(missing) err = %v; want no-match error", err)
	}
}

func TestResolveSessionFilePrefersIndex(t *testing.T) {
	root := t.TempDir()
	setEmptyHomes(t, root)

	dir := filepath.Join(root, "projects", "-repo-alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "aabbccdd-0000-0000-0000-000000000000.jsonl")
	if err := os.WriteFile(session, []byte(`{"type":"user"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"version":1,"originalPath":"/repo/alpha","entries":[` +
		`{"sessionId":"aabbccdd-0000-0000-0000-000000000000","fullPath":` + jsonString(session) + `,` +
		`"projectPath":"/repo/alpha","messageCount":1,` +
		`"created":"` + time.Now().Format(time.RFC3339Nano) + `",` +
		`"modified":"` + time.Now().Format(time.RFC3339Nano) + `"}]}`
	if err := os.WriteFile(filepath.Join(dir, "sessions-index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveSessionFile(context.Background(), "aabbccdd", 24*time.Hour)
	if err != nil || got != session {
		t.Errorf("ResolveSessionFile(indexed) = %q, %v; want %q", got, err, session)
	}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
