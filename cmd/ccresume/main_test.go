package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tmc/cc"
)

func TestResumeInvocation(t *testing.T) {
	tests := []struct {
		name     string
		entry    cc.IndexEntry
		wantBin  string
		wantArgs []string
	}{
		{
			name: "codex",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".codex", "sessions", "2026", "02", "25", "rollout.jsonl"),
				SessionID: "codex-session-id",
			},
			wantBin:  "codex",
			wantArgs: []string{"resume", "codex-session-id"},
		},
		{
			name: "gemini",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".gemini", "projects", "p", "s.jsonl"),
				SessionID: "gemini-session-id",
			},
			wantBin:  "gemini",
			wantArgs: []string{"-r", "gemini-session-id"},
		},
		{
			name: "opencode",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", "opencode", "storage", "session", "p", "ses_resume.json"),
				SessionID: "ses_resume",
			},
			wantBin:  "opencode",
			wantArgs: []string{"--session", "ses_resume"},
		},
		{
			name: "pi",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".pi", "agent", "sessions", "--p--", "ts_id.jsonl"),
				SessionID: "pi-session-id",
			},
			wantBin:  "pi",
			wantArgs: []string{"--session", filepath.Join("/tmp", ".pi", "agent", "sessions", "--p--", "ts_id.jsonl")},
		},
		{
			name: "antigravity",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".gemini", "antigravity", "brain", "conv-uuid"),
				SessionID: "ag-conv-uuid",
			},
			wantBin:  "agy",
			wantArgs: []string{"--conversation", "conv-uuid"},
		},
		{
			name: "antigravity from path",
			entry: cc.IndexEntry{
				FullPath: filepath.Join("/tmp", ".gemini", "antigravity-cli", "brain", "conv-uuid", ".system_generated", "logs", "transcript.jsonl"),
			},
			wantBin:  "agy",
			wantArgs: []string{"--conversation", "conv-uuid"},
		},
		{
			name: "claude",
			entry: cc.IndexEntry{
				FullPath:  filepath.Join("/tmp", ".claude", "projects", "p", "s.jsonl"),
				SessionID: "claude-session-id",
			},
			wantBin:  "claude",
			wantArgs: []string{"-r", "claude-session-id"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBin, gotArgs := resumeInvocation(tt.entry)
			if gotBin != tt.wantBin {
				t.Fatalf("bin = %q, want %q", gotBin, tt.wantBin)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Fatalf("args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestRenderResumeCommand(t *testing.T) {
	got := renderResumeCommand("/work/proj", "codex", []string{"resume", "sid-123"})
	want := "cd /work/proj; codex resume sid-123"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestRenderResumeCommandQuotesSpaces(t *testing.T) {
	got := renderResumeCommand("/Volumes/My Disk/proj", "claude", []string{"-r", "sid 1"})
	want := "cd '/Volumes/My Disk/proj'; claude -r 'sid 1'"
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestRenderResumeCommandQuotesShellMetachars(t *testing.T) {
	got := renderResumeCommand("/work/proj's; rm -rf /", "claude", nil)
	want := `cd '/work/proj'\''s; rm -rf /'; claude`
	if got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
}

func TestResolveMatchPrefersLatestExistingCWD(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "stale-does-not-exist")
	live := filepath.Join(dir, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": stale},
		{"sessionId": "s1", "cwd": live},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: stale})
	if r.target != live {
		t.Fatalf("target = %q, want %q", r.target, live)
	}
}

func TestResolveMatchPrefersExistingProjectPathOverLaterCWD(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	nested := filepath.Join(project, "subdir")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": project},
		{"sessionId": "s1", "cwd": nested},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: project})
	if r.target != project {
		t.Fatalf("target = %q, want %q", r.target, project)
	}
}

func TestResolveMatchIgnoresCodexSessionStorageProjectPath(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	storage := filepath.Join(dir, ".codex", "sessions", "2026", "05", "23")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(storage, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(storage, "rollout-2026-05-23T00-00-00-s1.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": project},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: storage})
	if r.target != project {
		t.Fatalf("target = %q, want %q", r.target, project)
	}
}

func TestResolveMatchFallsBackToProjectPath(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone-1")
	gone2 := filepath.Join(dir, "gone-2")
	session := filepath.Join(dir, "session.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"sessionId": "s1", "cwd": gone},
		{"sessionId": "s1", "cwd": gone2},
	})

	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: dir})
	if r.target != dir {
		t.Fatalf("target = %q, want %q", r.target, dir)
	}
}

func TestPreferredProjectPathUsesHomeGoSrcSymlink(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	target := filepath.Join(root, "vol", "go", "src")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "github.com", "tmc", "appledocs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "go", "src")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got := preferredProjectPath(filepath.Join(target, "github.com", "tmc", "appledocs"))
	want := filepath.Join(home, "go", "src", "github.com", "tmc", "appledocs")
	if got != want {
		t.Fatalf("preferredProjectPath = %q, want %q", got, want)
	}
}

func TestAntigravityConversationIDFromPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "session directory",
			path: filepath.Join("/home", ".gemini", "antigravity", "brain", "abc-123"),
			want: "abc-123",
		},
		{
			name: "nested transcript file",
			path: filepath.Join("/home", ".gemini", "antigravity", "brain", "abc-123", ".system_generated", "logs", "transcript.jsonl"),
			want: "abc-123",
		},
		{
			name: "cli brain root",
			path: filepath.Join("/home", ".gemini", "antigravity-cli", "brain", "def-456", "task.md"),
			want: "def-456",
		},
		{
			name: "no brain segment",
			path: filepath.Join("/home", ".claude", "projects", "p", "s.jsonl"),
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := antigravityConversationIDFromPath(tt.path); got != tt.want {
				t.Fatalf("antigravityConversationIDFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestCountOccurrences(t *testing.T) {
	tests := []struct {
		name  string
		query string
		text  string
		want  int
	}{
		{
			name:  "case insensitive",
			query: "rdma",
			text:  "RDMA setup mentioned rdma twice",
			want:  2,
		},
		{
			name:  "non overlapping",
			query: "aa",
			text:  "aaaa",
			want:  2,
		},
		{
			name:  "empty query",
			query: "",
			text:  "rdma",
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countOccurrences(tt.query, tt.text); got != tt.want {
				t.Fatalf("countOccurrences(%q, %q) = %d, want %d", tt.query, tt.text, got, tt.want)
			}
		})
	}
}

func TestNormalizeClaudeURL(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "artifact url with org query",
			query: "https://claude.ai/code/artifact/00000010-1000-4000-8000-000000000002?org=8c103e58-d2b3-4b6b-acce-a980aff9815e",
			want:  "00000010-1000-4000-8000-000000000002",
		},
		{
			name:  "artifact url with fragment",
			query: "https://claude.ai/code/artifact/abc123#top",
			want:  "abc123",
		},
		{
			name:  "session url",
			query: "https://claude.ai/code/session_011vNeoaAt8eZGAi7MWbQtnn",
			want:  "session_011vNeoaAt8eZGAi7MWbQtnn",
		},
		{
			name:  "public artifact url",
			query: "https://claude.ai/public/artifacts/4ad70609-2540-4a34-bd81-8f151278ecf4",
			want:  "4ad70609-2540-4a34-bd81-8f151278ecf4",
		},
		{
			name:  "design url",
			query: "https://claude.ai/design/my-slug",
			want:  "my-slug",
		},
		{
			name:  "bare id passes through",
			query: "00000010-1000-4000-8000-000000000002",
			want:  "00000010-1000-4000-8000-000000000002",
		},
		{
			name:  "non-claude url passes through",
			query: "https://example.com/code/artifact/abc",
			want:  "https://example.com/code/artifact/abc",
		},
		{
			name:  "unrecognized claude path passes through",
			query: "https://claude.ai/login",
			want:  "https://claude.ai/login",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeClaudeURL(tt.query); got != tt.want {
				t.Fatalf("normalizeClaudeURL(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

func TestResolveIt2SID(t *testing.T) {
	// Fixture: pane PANE resolves to sess-new (most recent) over sess-old.
	const pane = "AAAAAAAA-1111-2222-3333-444444444444"
	root := t.TempDir()
	t.Setenv("IT2_HOME", root)
	dir := filepath.Join(root, "sessions", pane, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(sess, ts string) string {
		return `{"session_id":"` + sess + `","source":"startup","hook_event_name":"SessionStart",` +
			`"_it2":{"it2_sid":"` + pane + `","boot_id":"boot-1","ts":"` + ts + `"}}`
	}
	events := line("sess-old", "2026-07-19T09:00:00Z") + "\n" + line("sess-new", "2026-07-19T12:00:00Z") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code-events.ndjson"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, query, want string
	}{
		{"bare it2 sid resolves to newest session", pane, "sess-new"},
		{"prefixed it2 sid resolves too", "w0t0p4:" + pane, "sess-new"},
		{"unknown pane passes through", "BBBBBBBB-1111-2222-3333-444444444444", "BBBBBBBB-1111-2222-3333-444444444444"},
		{"non-sid query passes through", "kafka rebalance", "kafka rebalance"},
		{"artifact uuid-shaped but not a bound pane passes through", "00000010-1000-4000-8000-000000000002", "00000010-1000-4000-8000-000000000002"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveIt2SID(tt.query); got != tt.want {
				t.Errorf("resolveIt2SID(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}

// TestResolveIt2SIDCrossBoot: a pane whose binding to one session spans a reboot
// (two boot_ids, same session_id + it2_sid) is a single continuous binding, so
// the pane still resolves to that session. Guards the boot-metadata fix.
func TestResolveIt2SIDCrossBoot(t *testing.T) {
	const pane = "CCCCCCCC-1111-2222-3333-444444444444"
	root := t.TempDir()
	t.Setenv("IT2_HOME", root)
	dir := filepath.Join(root, "sessions", pane, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(boot, ts string) string {
		return `{"session_id":"sess-restored","source":"resume","hook_event_name":"SessionStart",` +
			`"_it2":{"it2_sid":"` + pane + `","boot_id":"` + boot + `","ts":"` + ts + `"}}`
	}
	events := line("boot-1", "2026-07-19T09:00:00Z") + "\n" + line("boot-2", "2026-07-20T09:00:00Z") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code-events.ndjson"), []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveIt2SID(pane); got != "sess-restored" {
		t.Errorf("resolveIt2SID(%q) = %q, want sess-restored (binding continuous across boots)", pane, got)
	}
}

func TestFilterMatches(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, ".codex")
	claude := filepath.Join(root, ".claude")
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	dir := filepath.Join(root, "work")
	other := filepath.Join(root, "other")
	codexPath := filepath.Join(codex, "sessions", "2026", "07", "12", "rollout.jsonl")
	claudePath := filepath.Join(claude, "projects", "work", "session.jsonl")
	for _, path := range []string{codexPath, claudePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJSONL(t, codexPath, []map[string]any{{"type": "session_meta", "payload": map[string]any{"id": "codex-id", "cwd": dir}}})
	writeJSONL(t, claudePath, []map[string]any{{"sessionId": "claude-id", "cwd": other}})

	matches := []cc.IndexEntry{
		{FullPath: codexPath, ProjectPath: filepath.Dir(codexPath)},
		{FullPath: claudePath, ProjectPath: other},
	}
	got := filterMatches(matches, dir, "codex")
	if len(got) != 1 || got[0].FullPath != codexPath {
		t.Fatalf("filterMatches = %#v, want codex session", got)
	}
}

func TestGrepMatchesLiteralQuery(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	root := t.TempDir()
	t.Setenv("CLAUDE_HOME", filepath.Join(root, ".claude"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, ".gemini"))
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, ".antigravity"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, ".opencode"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, ".pi"))
	path := filepath.Join(root, ".claude", "projects", "work", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Queries are plain text, not regexps: metacharacters like the unbalanced
	// paren here must match literally instead of failing rg's regex parser.
	const query = "code didn't compile (empirically reproduced"
	writeJSONL(t, path, []map[string]any{{"sessionId": "test-id", "cwd": filepath.Join(root, "work"), "text": query}})
	matches, err := grepMatches(query)
	if err != nil {
		t.Fatalf("grepMatches: %v", err)
	}
	if len(matches) != 1 || matches[0].FullPath != path {
		t.Fatalf("grepMatches = %#v, want single match for %s", matches, path)
	}
}

func TestCleanHarness(t *testing.T) {
	got, err := cleanHarness(" Codex ")
	if err != nil || got != "codex" {
		t.Fatalf("cleanHarness = %q, %v, want codex, nil", got, err)
	}
	if _, err := cleanHarness("vim"); err == nil {
		t.Fatal("cleanHarness(vim) succeeded")
	}
}

func TestFileHasToolMatchCommandAndResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := []byte(
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"it2 session current"}}]}}` + "\n" +
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"052AB527-F732-4D5A-A35D-D72E2EF98137"}]}}` + "\n")
	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatal(err)
	}
	ok, err := fileHasToolMatch(path, "it2 session current", "052AB527")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("tool command/result match not found")
	}
	ok, err = fileHasToolMatch(path, "it2 session current", "missing-session")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unexpected tool command/result match")
	}
}

func TestRecentMatchingPathsIncludesOpenCodeSessions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "session")
	path := filepath.Join(root, "project", "ses_recent.json")
	writeTextFile(t, path, `{"id":"ses_recent","directory":"/work/recent","title":"needle opencode"}`)

	got, err := recentMatchingPaths([]searchRoot{{dir: root, kind: "opencode"}}, "needle opencode", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != path {
		t.Fatalf("recentMatchingPaths = %#v, want %q", got, path)
	}
}

func TestIndexEntryForOpenCodePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "session")
	path := filepath.Join(root, "project", "ses_index.json")
	writeTextFile(t, path, `{"id":"ses_index","directory":"/work/index"}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := indexEntryForPath(path, searchRoot{dir: root, kind: "opencode"}, info)
	if !ok {
		t.Fatal("indexEntryForPath returned false")
	}
	if got.SessionID != "ses_index" || got.ProjectPath != "/work/index" || got.FullPath != path {
		t.Fatalf("indexEntryForPath = %#v", got)
	}
}

func TestIndexEntryForPiPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "--work-pi--", "2026-04-19T00-00-00-000Z_idx.jsonl")
	writeTextFile(t, path, `{"type":"session","id":"pi-idx","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/work/pi"}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := indexEntryForPath(path, searchRoot{dir: root, kind: "pi"}, info)
	if !ok {
		t.Fatal("indexEntryForPath returned false")
	}
	if got.SessionID != "pi-idx" || got.ProjectPath != "/work/pi" || got.FullPath != path {
		t.Fatalf("indexEntryForPath = %#v", got)
	}
}

func writeJSONL(t *testing.T, path string, lines []map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}
}

func writeTextFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
		t.Fatal(err)
	}
}
