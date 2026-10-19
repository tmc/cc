package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestExtractIt2SID(t *testing.T) {
	const (
		fullUUID = "A28AB5E5-B849-4F2B-8CD4-45E0AA377AB4"
		sendLine = "[it2:send-text src=A5B2B529 dst=481499F5]"
	)
	tests := []struct {
		name, query, side, want string
	}{
		{"send-text src default", sendLine, "src", "A5B2B529"},
		{"send-text dst override", sendLine, "dst", "481499F5"},
		{"send-key src", "[it2:send-key src=AABBCCDD dst=11223344]", "src", "AABBCCDD"},
		{"collab-msg src", `<collab-msg src="` + fullUUID + `">`, "src", fullUUID},
		{"collab-msg dst falls back to src", `<collab-msg src="` + fullUUID + `">`, "dst", fullUUID},
		{"wNtNpN prefix on src stripped-tolerated", "[it2:send-text src=w0t0p4:AABBCCDD dst=11223344]", "src", "AABBCCDD"},
		{"wNtNpN prefix on dst", "[it2:send-text src=AABBCCDD dst=w1t2p3:11223344]", "dst", "11223344"},
		{"two markers: first only", sendLine + " then [it2:send-text src=99999999 dst=88888888]", "src", "A5B2B529"},
		{"no marker: plain text unchanged", "kafka rebalance", "src", "kafka rebalance"},
		{"no marker: claude url unchanged", "https://claude.ai/code/artifact/abc123", "src", "https://claude.ai/code/artifact/abc123"},
		{"no marker: bare sid unchanged (idempotent)", fullUUID, "src", fullUUID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractIt2SID(tt.query, tt.side); got != tt.want {
				t.Errorf("extractIt2SID(%q, %q) = %q, want %q", tt.query, tt.side, got, tt.want)
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

// TestExtractThenResolveIt2SID exercises the pipeline order: a copied send-text
// line whose src pane has a binding resolves through extractIt2SID ->
// resolveIt2SID to the bound session; with no binding it falls through to the
// bare SID for content search rather than erroring.
func TestExtractThenResolveIt2SID(t *testing.T) {
	const pane = "DDDDDDDD-1111-2222-3333-444444444444"
	root := t.TempDir()
	t.Setenv("IT2_HOME", root)
	dir := filepath.Join(root, "sessions", pane, "artifacts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"session_id":"sess-bound","source":"startup","hook_event_name":"SessionStart",` +
		`"_it2":{"it2_sid":"` + pane + `","boot_id":"boot-1","ts":"2026-07-19T10:00:00Z"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-code-events.ndjson"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	// A send-text line whose src is the bound pane resolves to its session.
	marker := "[it2:send-text src=" + pane + " dst=99999999-1111-2222-3333-444444444444]"
	if got := resolveIt2SID(extractIt2SID(marker, "src")); got != "sess-bound" {
		t.Errorf("pipeline(%q) = %q, want sess-bound", marker, got)
	}
	// An unbound pane in the marker falls through to the bare SID (for content search).
	unbound := "[it2:send-text src=EEEEEEEE-1111-2222-3333-444444444444 dst=" + pane + "]"
	if got := resolveIt2SID(extractIt2SID(unbound, "src")); got != "EEEEEEEE-1111-2222-3333-444444444444" {
		t.Errorf("pipeline(%q) = %q, want the bare src SID for content search", unbound, got)
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
	matches, err := recentContentMatches(query, time.Hour)
	if err != nil {
		t.Fatalf("recentContentMatches: %v", err)
	}
	if len(matches) != 1 || matches[0].FullPath != path {
		t.Fatalf("recentContentMatches = %#v, want single match for %s", matches, path)
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

func TestRenderSpark(t *testing.T) {
	tests := []struct {
		name   string
		counts []int
		want   string
	}{
		{"all zero", []int{0, 0, 0, 0}, "▁▁▁▁"},
		{"single nonzero bucket", []int{0, 0, 5, 0}, "▁▁█▁"},
		{"monotonic ramp", []int{0, 1, 2, 3, 4, 5, 6, 7}, "▁▂▃▄▅▆▇█"},
		{"scaling: max 10, mid 5", []int{10, 5, 0}, "█▄▁"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderSpark(tt.counts); got != tt.want {
				t.Errorf("renderSpark(%v) = %q, want %q", tt.counts, got, tt.want)
			}
		})
	}
}

// tsLine builds one transcript line with a timestamp and body text.
func tsLine(ts, body string) string {
	return `{"timestamp":"` + ts + `","text":"` + body + `"}`
}

func TestOccurrenceSparkline(t *testing.T) {
	// A span of 09:00 -> 17:00 (8h) with 8 buckets => 1h per bucket.
	base := "2026-07-19T"
	writeTranscript := func(t *testing.T, lines ...string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("early only", func(t *testing.T) {
		// Match at the very start and a bare span-end anchor with no match.
		p := writeTranscript(t,
			tsLine(base+"09:00:00Z", "widget here"),
			tsLine(base+"17:00:00Z", "nothing"),
		)
		got, _ := occurrenceInfo("widget", p, 8)
		if !strings.HasPrefix(got, "█") || got[len("█"):] != strings.Repeat("▁", 7) {
			t.Errorf("early-only = %q, want █ then 7×▁", got)
		}
	})

	t.Run("late only", func(t *testing.T) {
		p := writeTranscript(t,
			tsLine(base+"09:00:00Z", "nothing"),
			tsLine(base+"17:00:00Z", "widget here"),
		)
		got, _ := occurrenceInfo("widget", p, 8)
		want := strings.Repeat("▁", 7) + "█"
		if got != want {
			t.Errorf("late-only = %q, want %q", got, want)
		}
	})

	t.Run("idle gap: two clusters, empty middle", func(t *testing.T) {
		// Cluster at start, cluster at end, long empty gap => middle buckets ▁.
		p := writeTranscript(t,
			tsLine(base+"09:00:00Z", "widget"),
			tsLine(base+"09:01:00Z", "widget"),
			tsLine(base+"17:00:00Z", "widget"),
		)
		got, _ := occurrenceInfo("widget", p, 8)
		r := []rune(got)
		if len(r) != 8 {
			t.Fatalf("want 8 glyphs, got %d (%q)", len(r), got)
		}
		if r[0] != '█' || r[7] == '▁' {
			t.Errorf("clusters = %q, want first bucket full and last bucket non-empty", got)
		}
		for i := 2; i <= 6; i++ {
			if r[i] != '▁' {
				t.Errorf("middle bucket %d = %q, want ▁ (idle gap), full=%q", i, string(r[i]), got)
			}
		}
	})

	t.Run("lines without timestamp are skipped", func(t *testing.T) {
		p := writeTranscript(t,
			`{"text":"widget with no timestamp"}`,
			tsLine(base+"09:00:00Z", "widget"),
			`garbage not json widget`,
			tsLine(base+"17:00:00Z", "widget"),
		)
		// Should not crash; only the two timestamped matches bucket.
		got, _ := occurrenceInfo("widget", p, 8)
		if len([]rune(got)) != 8 {
			t.Errorf("got %q, want 8 glyphs (untimed lines skipped, no crash)", got)
		}
	})

	t.Run("no match returns empty", func(t *testing.T) {
		p := writeTranscript(t, tsLine(base+"09:00:00Z", "nothing here"))
		if got, _ := occurrenceInfo("widget", p, 8); got != "" {
			t.Errorf("no-match = %q, want empty", got)
		}
	})
}

func TestInfoAnnotationIDResolutionHasNoSparkline(t *testing.T) {
	// Build a match with occurrences but flag it as an id-resolution: no hits/sparkline.
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(tsLine("2026-07-19T09:00:00Z", "widget")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := resolvedMatch{
		entry:       cc.IndexEntry{FullPath: p, MessageCount: 42, Modified: "2026-07-19T13:54:00Z"},
		summary:     cc.SessionSummary{TotalLines: 100},
		occurrences: 3,
	}
	got := infoAnnotation("widget", r, true, false)
	if strings.ContainsAny(got, "▁▂▃▄▅▆▇█") || strings.Contains(got, "%") || strings.Contains(got, "@") {
		t.Errorf("id-resolution annotation = %q, want no sparkline/density/hits", got)
	}
	if !strings.Contains(got, "42×") {
		t.Errorf("annotation = %q, want size (42×) present", got)
	}
	// A content query on the same match DOES get a sparkline + density.
	got2 := infoAnnotation("widget", r, false, false)
	if !strings.Contains(got2, "%") || !strings.ContainsAny(got2, "▁▂▃▄▅▆▇█") {
		t.Errorf("content annotation = %q, want density + sparkline", got2)
	}
}

func TestInfoAnnotationSessionName(t *testing.T) {
	r := resolvedMatch{
		entry:   cc.IndexEntry{MessageCount: 42, Modified: "2026-07-19T13:54:00Z"},
		summary: cc.SessionSummary{CustomTitle: "main appledocs coordinator"},
	}
	got := infoAnnotation("", r, true, false)
	if !strings.HasSuffix(got, ` "main appledocs coordinator"`) {
		t.Errorf("plain annotation = %q, want quoted name after the stats", got)
	}
	colored := infoAnnotation("", r, true, true)
	if !strings.Contains(colored, ansiReverse+" main appledocs coordinator "+ansiReset) {
		t.Errorf("colored annotation = %q, want reverse-video padded name", colored)
	}
	// No name → no leading name token.
	r.summary.CustomTitle = ""
	if got := infoAnnotation("", r, true, false); !strings.HasPrefix(got, "42×") {
		t.Errorf("unnamed annotation = %q, want to start with size", got)
	}
}

func TestOccurrenceInfoDensity(t *testing.T) {
	base := "2026-07-19T"
	write := func(t *testing.T, lines ...string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("every line matches: 100 percent", func(t *testing.T) {
		p := write(t,
			tsLine(base+"09:00:00Z", "widget"),
			tsLine(base+"10:00:00Z", "widget again"),
		)
		_, dense := occurrenceInfo("widget", p, 8)
		if dense != 100 {
			t.Errorf("dense = %d, want 100", dense)
		}
	})

	t.Run("one hit-line of 100: 1 percent", func(t *testing.T) {
		lines := make([]string, 100)
		for i := range lines {
			body := "nothing"
			if i == 42 {
				body = "widget"
			}
			// spread timestamps across the hour so all are distinct-ish
			lines[i] = tsLine(base+fmt.Sprintf("09:%02d:00Z", i%60), body)
		}
		p := write(t, lines...)
		_, dense := occurrenceInfo("widget", p, 8)
		if dense != 1 {
			t.Errorf("dense = %d, want 1", dense)
		}
	})

	t.Run("no match: zero", func(t *testing.T) {
		p := write(t, tsLine(base+"09:00:00Z", "nothing"))
		spark, dense := occurrenceInfo("widget", p, 8)
		if spark != "" || dense != 0 {
			t.Errorf("no-match = (%q, %d), want (\"\", 0)", spark, dense)
		}
	})
}

func TestDimGating(t *testing.T) {
	s := "# 10 msgs · 3 hits"
	if got := dim(s, false); got != s {
		t.Errorf("dim off = %q, want unchanged (byte-clean, no ESC)", got)
	}
	if strings.ContainsRune(dim(s, false), '\033') {
		t.Error("dim off must contain no ESC bytes (pipe/copy safe)")
	}
	on := dim(s, true)
	if !strings.HasPrefix(on, ansiDim) || !strings.HasSuffix(on, ansiReset) {
		t.Errorf("dim on = %q, want wrapped in dim/reset", on)
	}
}

func TestInfoAnnotationHasDensityToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	// 2 timestamped lines, 1 matches -> 50% dense.
	body := tsLine("2026-07-19T09:00:00Z", "widget") + "\n" + tsLine("2026-07-19T10:00:00Z", "nothing") + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := resolvedMatch{
		entry:       cc.IndexEntry{FullPath: p, MessageCount: 2, Modified: "2026-07-19T13:54:00Z"},
		summary:     cc.SessionSummary{TotalLines: 2},
		occurrences: 1,
	}
	got := infoAnnotation("widget", r, false, false)
	if !strings.Contains(got, "50%") {
		t.Errorf("annotation = %q, want a '50%%' density token", got)
	}
	// id-resolution: no density token.
	if id := infoAnnotation("widget", r, true, false); strings.Contains(id, "%") {
		t.Errorf("id-resolution annotation = %q, want no density token", id)
	}
}

func TestHumanize(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"}, {42, "42"}, {896, "896"}, {999, "999"},
		{2123, "2.1k"}, {9999, "10.0k"}, {10000, "10k"},
		{79853, "80k"}, {999999, "1000k"},
		{1_200_000, "1.2M"}, {12_925_000, "13M"},
	}
	for _, tt := range tests {
		if got := humanize(tt.n); got != tt.want {
			t.Errorf("humanize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestRelAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{2 * 24 * time.Hour, "2d"},
		{3 * 7 * 24 * time.Hour, "3w"},
	}
	for _, tt := range tests {
		if got := relAge(now.Add(-tt.d)); got != tt.want {
			t.Errorf("relAge(-%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
	if got := relAge(time.Time{}); got != "?" {
		t.Errorf("relAge(zero) = %q, want ?", got)
	}
}

func TestValidSessionID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"df77efb9-9817-4c6e-aacf-86118b39ec38", true}, // a normal UUID session
		{"019f440c-1234-7abc-def0-112233445566", true}, // codex rollout id
		{"agent-a702817e4f621e755", false},             // subagent transcript
		{"agent-a613c4e5f6c498f32", false},             // subagent transcript
		{"agent-acompact-abc123", false},               // compaction subagent
	}
	for _, tt := range tests {
		if got := validSessionID(tt.id); got != tt.want {
			t.Errorf("validSessionID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestHomeQuote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tests := []struct {
		dir  string
		want string
	}{
		{home, "~"},
		{filepath.Join(home, "go", "src", "x"), "~/go/src/x"},
		{filepath.Join(home, "a b"), "~/'a b'"}, // metachars in the rest stay quoted
		{"/opt/other", "/opt/other"},            // not under home: unchanged
	}
	for _, tt := range tests {
		if got := homeQuote(tt.dir); got != tt.want {
			t.Errorf("homeQuote(%q) = %q, want %q", tt.dir, got, tt.want)
		}
	}
}

func TestSortIndexMatchesRecency(t *testing.T) {
	old := *sortFlag
	*sortFlag = "recency"
	defer func() { *sortFlag = old }()

	matches := []cc.IndexEntry{
		{SessionID: "older", FullPath: "/a", Modified: "2026-07-01T00:00:00Z", FirstPrompt: "needle needle needle"},
		{SessionID: "newer", FullPath: "/b", Modified: "2026-07-02T00:00:00Z", FirstPrompt: "needle"},
	}
	sortIndexMatches("needle", matches, "")
	if matches[0].SessionID != "newer" {
		t.Errorf("recency sort: first = %q, want %q", matches[0].SessionID, "newer")
	}
}

func TestResolveMatchDropsAntigravityBrainTarget(t *testing.T) {
	dir := t.TempDir()
	brain := filepath.Join(dir, ".gemini", "antigravity-cli", "brain", "conv-uuid")
	logs := filepath.Join(brain, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(logs, "transcript.jsonl")
	writeJSONL(t, session, []map[string]any{
		{"step_index": 0, "type": "USER_INPUT", "content": "hello"},
	})

	// No recorded cwd exists, so resolution would otherwise fall back to the
	// brain storage dir; that must be dropped rather than used as a workspace.
	r := resolveMatch("", cc.IndexEntry{FullPath: session, ProjectPath: brain})
	if r.target != "" {
		t.Fatalf("target = %q, want empty (brain storage is not a workspace)", r.target)
	}
}

func TestRawSessionPathUsesAntigravityFullTranscript(t *testing.T) {
	brain := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli", "brain", "conv-uuid")
	logs := filepath.Join(brain, ".system_generated", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	compact := filepath.Join(logs, "transcript.jsonl")
	full := filepath.Join(logs, "transcript_full.jsonl")
	writeTextFile(t, compact, "compact\n")
	writeTextFile(t, full, "full\n")
	if got := rawSessionPath(brain); got != full {
		t.Fatalf("rawSessionPath = %q, want %q", got, full)
	}
	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if got := rawSessionPath(brain); got != compact {
		t.Fatalf("rawSessionPath without full = %q, want %q", got, compact)
	}
}

func TestIndexEntryForCodexPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "09", "10", "rollout-2026-09-10T08-00-00-01a088eb-04da-7fb0-a8e6-f6700080b679.jsonl")
	writeTextFile(t, path, `{"type":"session_meta","payload":{"session_id":"01a088eb-04da-7fb0-a8e6-f6700080b679","id":"01a088eb-04da-7fb0-a8e6-f6700080b679","cwd":"/work/codex","thread_source":"user"}}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := indexEntryForPath(path, searchRoot{dir: root, kind: "codex"}, info)
	if !ok {
		t.Fatal("indexEntryForPath returned false")
	}
	if got.SessionID != "01a088eb-04da-7fb0-a8e6-f6700080b679" || got.ProjectPath != "/work/codex" {
		t.Fatalf("indexEntryForPath = %#v", got)
	}
}

func TestIndexEntryForCodexSubagentPath(t *testing.T) {
	// A subagent rollout carries its parent's session_id; offering it would
	// both duplicate the parent and name a thread codex cannot resume alone.
	root := filepath.Join(t.TempDir(), "sessions")
	path := filepath.Join(root, "2026", "09", "10", "rollout-2026-09-10T09-00-00-01a08914-d2b4-7983-9adb-31bb49c3a45f.jsonl")
	writeTextFile(t, path, `{"type":"session_meta","payload":{"session_id":"01a088eb-04da-7fb0-a8e6-f6700080b679","id":"01a08914-d2b4-7983-9adb-31bb49c3a45f","cwd":"/work/codex","thread_source":"subagent"}}`)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if got, ok := indexEntryForPath(path, searchRoot{dir: root, kind: "codex"}, info); ok {
		t.Fatalf("indexEntryForPath = %#v, want subagent rejected", got)
	}
}

func TestMetadataMatchesFindsClaudeAndCodexSessions(t *testing.T) {
	// Claude Code stopped writing sessions-index.json and codex never had one,
	// so both harnesses are found only by scanning the session files.
	root := t.TempDir()
	claudeRoot := filepath.Join(root, ".claude", "projects")
	claudePath := filepath.Join(claudeRoot, "-work-pumpfun-ml", "b1c30ab6-2b2f-4b0b-9a51-3f7c2c0f0d21.jsonl")
	writeTextFile(t, claudePath, `{"type":"user","cwd":"/work/pumpfun-ml"}`)

	codexRoot := filepath.Join(root, ".codex", "sessions")
	codexPath := filepath.Join(codexRoot, "2026", "09", "10", "rollout-2026-09-10T08-00-00-01a088eb-04da-7fb0-a8e6-f6700080b679.jsonl")
	writeTextFile(t, codexPath, `{"type":"session_meta","payload":{"id":"01a088eb-04da-7fb0-a8e6-f6700080b679","cwd":"/work/pumpfun-ml","thread_source":"user"}}`)

	roots := []searchRoot{{dir: claudeRoot, kind: "claude"}, {dir: codexRoot, kind: "codex"}}
	lists, err := recentCandidates(roots, 60)
	if err != nil {
		t.Fatal(err)
	}
	got := metadataMatches("pumpfun", roots, lists)
	found := map[string]bool{}
	for _, e := range got {
		found[e.FullPath] = true
	}
	if !found[claudePath] || !found[codexPath] {
		t.Fatalf("metadataMatches = %#v, want %s and %s", got, claudePath, codexPath)
	}
}

func TestSearchIgnoresClaudeWorkflowJournals(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	root := t.TempDir()
	project := filepath.Join(root, "-work-wt-ds4-min")
	path := filepath.Join(project, "session.jsonl")
	writeTextFile(t, path, `{"type":"user","text":"wt-ds4-min"}`)
	writeTextFile(t, filepath.Join(project, "session", "subagents", "workflows", "wf_1", "journal.jsonl"), `{"type":"started","text":"wt-ds4-min"}`)
	roots := []searchRoot{{dir: root, kind: "claude"}}
	lists, err := recentCandidates(roots, 60)
	if err != nil {
		t.Fatal(err)
	}
	meta := metadataMatches("ds4", roots, lists)
	content, err := contentMatches("ds4", roots, lists)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		got  []cc.IndexEntry
	}{
		{"metadata", meta},
		{"content", content},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.got) != 1 || tt.got[0].FullPath != path {
				t.Fatalf("matches = %#v, want only %s", tt.got, path)
			}
		})
	}
}

func TestMergeEntriesKeepsIndexedFields(t *testing.T) {
	indexed := []cc.IndexEntry{{SessionID: "a", FullPath: "/p/a.jsonl", Summary: "indexed"}}
	scanned := []cc.IndexEntry{{SessionID: "a", FullPath: "/p/a.jsonl"}, {SessionID: "b", FullPath: "/p/b.jsonl"}}
	got := mergeEntries(indexed, scanned)
	if len(got) != 2 || got[0].Summary != "indexed" || got[1].SessionID != "b" {
		t.Fatalf("mergeEntries = %#v", got)
	}
}

func TestRecentContentMatchesIgnoresQueryCase(t *testing.T) {
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
	// A part number is recalled, not copied, so the query rarely arrives in the
	// capitalization the transcript used.
	writeTextFile(t, path, `{"sessionId":"case-id","text":"the MCX516A-CCAT takes a QSFP28 cable"}`)

	for _, query := range []string{"qsfp28", "QSFP28", "QsFp28"} {
		matches, err := recentContentMatches(query, time.Hour)
		if err != nil {
			t.Fatalf("recentContentMatches(%q): %v", query, err)
		}
		if len(matches) != 1 || matches[0].FullPath != path {
			t.Fatalf("recentContentMatches(%q) = %#v, want %s", query, matches, path)
		}
	}
}

func TestFileContainsIgnoresCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTextFile(t, path, `{"text":"a QSFP28 transceiver"}`)
	for _, contains := range []string{"qsfp28", "QSFP28"} {
		ok, err := fileContains(path, contains)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("fileContains(%q) = false, want true", contains)
		}
	}
	if ok, err := fileContains(path, "sfp56"); err != nil || ok {
		t.Fatalf("fileContains(sfp56) = %v, %v, want false, nil", ok, err)
	}
}

func TestFileHasToolMatchIgnoresCase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := []byte(
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"ethtool -m enp1s0f0"}}]}}` + "\n" +
			`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"Identifier: QSFP28"}]}}` + "\n")
	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatal(err)
	}
	ok, err := fileHasToolMatch(path, "ETHTOOL -m", "qsfp28")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("tool command/result match not found for a differently cased query")
	}
}

func TestSearchRunWidensPastSinceWindow(t *testing.T) {
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
	path := filepath.Join(root, ".claude", "projects", "-work", "b1c30ab6-2b2f-4b0b-9a51-3f7c2c0f0d21.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const query = "expected stderr to match the actual message"
	writeJSONL(t, path, []map[string]any{{"sessionId": "b1c30ab6-2b2f-4b0b-9a51-3f7c2c0f0d21", "cwd": "/work", "text": query}})
	old := time.Now().Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	s := searchSpec{query: query}
	got, err := s.run(7 * 24 * time.Hour)
	if err != nil {
		t.Fatalf("run(7d): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("run(7d) = %#v, want no matches outside the window", got)
	}
	got, err = s.run(allHistory)
	if err != nil {
		t.Fatalf("run(allHistory): %v", err)
	}
	if len(got) != 1 || got[0].FullPath != path {
		t.Fatalf("run(allHistory) = %#v, want single match for %s", got, path)
	}
}

func TestResolveIt2SIDStoreVerdict(t *testing.T) {
	const pane = "AAAAAAAA-1111-2222-3333-444444444444"
	for _, tt := range []struct {
		name, events, query, code, want string
		directory, file                 bool
	}{
		{name: "absent", query: pane, code: "store-absent", want: pane},
		{name: "unwritten", query: pane, code: "store-unwritten", want: pane, directory: true},
		{name: "empty", query: pane, code: "store-empty", want: pane, directory: true, file: true},
		{name: "unbound", query: pane, code: "store-unbound", want: pane, directory: true, file: true, events: `{"session_id":"s"}`},
		{name: "bound", query: pane, want: "s", directory: true, file: true, events: `{"session_id":"s","hook_event_name":"SessionStart","_it2":{"it2_sid":"` + pane + `","boot_id":"b","ts":"2026-09-29T12:00:00Z"}}`},
		{name: "unknown pane", query: "BBBBBBBB-1111-2222-3333-444444444444", want: "BBBBBBBB-1111-2222-3333-444444444444", directory: true, file: true, events: `{"session_id":"s","hook_event_name":"SessionStart","_it2":{"it2_sid":"` + pane + `","boot_id":"b","ts":"2026-09-29T12:00:00Z"}}`},
		{name: "text", query: "hello", want: "hello"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "sessions")
			dir := filepath.Join(root, pane, "artifacts")
			if tt.directory {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.file {
				if err := os.WriteFile(filepath.Join(dir, "claude-code-events.ndjson"), []byte(tt.events), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			if got := resolveIt2SIDInStore(tt.query, root, &buf); got != tt.want {
				t.Fatalf("resolved = %q, want %q", got, tt.want)
			}
			if tt.code != "" && !strings.Contains(buf.String(), tt.code+":") {
				t.Fatalf("diagnostic = %q, want %s", buf.String(), tt.code)
			}
			if tt.want == "s" || tt.query == "hello" {
				if buf.Len() != 0 {
					t.Fatalf("unexpected diagnostic: %s", &buf)
				}
			} else if !strings.Contains(buf.String(), "searching transcript content") {
				t.Fatalf("missing fallback: %s", &buf)
			}
		})
	}
}
