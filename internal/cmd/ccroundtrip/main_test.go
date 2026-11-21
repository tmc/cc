package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc"
)

func TestEqualNormalizedJSONSpelling(t *testing.T) {
	a := cc.Entry{Message: &cc.Message{Role: "user", Content: json.RawMessage(`"<collab-msg>"`)}}
	b := cc.Entry{Message: &cc.Message{Role: "user", Content: json.RawMessage(`"\u003ccollab-msg\u003e"`)}}
	if !equalNormalized(a, b) {
		t.Fatal("equivalent JSON escapes compare unequal")
	}
	b.Message.Content = json.RawMessage(`"different"`)
	if equalNormalized(a, b) {
		t.Fatal("different JSON values compare equal")
	}
}

func TestRunAllHarnesses(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "claude")
	geminiHome := filepath.Join(root, "gemini")
	codexHome := filepath.Join(root, "codex")
	t.Setenv("CLAUDE_HOME", claudeHome)
	t.Setenv("GEMINI_HOME", geminiHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, "antigravity"))

	claude := filepath.Join(claudeHome, "projects", "-project", "session.jsonl")
	gemini := filepath.Join(geminiHome, "projects", "-project", "session.jsonl")
	codex := filepath.Join(codexHome, "sessions", "2026", "07", "22", "rollout.jsonl")
	copyFixture(t, filepath.Join("..", "..", "..", "cmd", "ccfmt", "testdata", "claude-compaction.jsonl"), claude)
	copyFixture(t, filepath.Join("..", "..", "..", "cmd", "ccfmt", "testdata", "claude-compaction.jsonl"), gemini)
	copyFixture(t, filepath.Join("..", "..", "..", "testdata", "sample-session.jsonl"), codex)

	paths := []string{
		claude,
		codex,
		gemini,
		filepath.Join("testdata", "agy", ".system_generated", "logs", "transcript.jsonl"),
		filepath.Join("..", "..", "..", "testdata", "opencode-compaction", "storage", "session", "proj", "ses_compact.json"),
		filepath.Join("..", "..", "..", "testdata", "pi", "agent", "sessions", "proj", "pi-compaction.jsonl"),
	}

	var stdout, stderr bytes.Buffer
	args := append([]string{"-json"}, paths...)
	if err := run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if len(reports) != 6 {
		t.Fatalf("got %d reports, want 6: %+v", len(reports), reports)
	}
	seen := make(map[string]bool)
	for _, report := range reports {
		if report.Error != "" || report.Entries == 0 {
			t.Errorf("report = %+v", report)
		}
		seen[report.Harness] = true
	}
	for _, harness := range []string{"claude", "codex", "gemini", "opencode", "pi", "antigravity"} {
		if !seen[harness] {
			t.Errorf("missing %s report: %+v", harness, reports)
		}
	}
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRunAntigravity(t *testing.T) {
	path := filepath.Join("testdata", "agy")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	if got := strings.Count(stdout.String(), "\nok ") + strings.Count(stdout.String(), "ok "); got != 1 {
		t.Fatalf("reported %d histories, want 1:\n%s", got, stdout.String())
	}
	for _, want := range []string{"harness=antigravity", "tools=1", "results=1", "truncated=3"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunJSON(t *testing.T) {
	path := filepath.Join("testdata", "agy", ".system_generated", "logs", "transcript.jsonl")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-json", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if len(reports) != 1 || reports[0].Error != "" || reports[0].TruncatedFields != 3 {
		t.Fatalf("reports = %+v", reports)
	}
}

func TestSessionFilesSkipsAntigravityFullCompanion(t *testing.T) {
	paths, err := sessionFiles([]string{filepath.Join("testdata", "agy")})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "transcript.jsonl" {
		t.Fatalf("paths = %v, want compact transcript only", paths)
	}
}

func TestRunGeminiCLI(t *testing.T) {
	chats := filepath.Join(t.TempDir(), "chats")
	if err := os.Mkdir(chats, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chats, "session-2026-04-03T22-57-d00a4143.json")
	session := `{
		"sessionId": "d00a4143-e123-42c2-a19f-874687c50621",
		"startTime": "2026-04-03T22:57:32.508Z",
		"messages": [
			{"id":"user-1","type":"user","content":"hello"},
			{"id":"model-1","type":"gemini","content":"inspecting","toolCalls":[
				{"id":"call-1","name":"read_file","args":{"file_path":"main.go"},
				"result":[{"functionResponse":{"response":{"output":"package main"}}}],
				"status":"success"}
			]}
		]
	}`
	if err := os.WriteFile(path, []byte(session), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-strict", "-json", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s\n%s", err, stderr.String(), stdout.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	want := report{Path: path, Harness: "gemini", Entries: 4, ToolUses: 1, ToolResults: 1}
	if len(reports) != 1 || reports[0] != want {
		t.Fatalf("reports = %+v, want [%+v]", reports, want)
	}
}

func TestRunAntigravityFullOnly(t *testing.T) {
	relative := filepath.Join(".system_generated", "logs", "transcript_full.jsonl")
	path := filepath.Join(t.TempDir(), relative)
	copyFixture(t, filepath.Join("testdata", "agy", relative), path)
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-strict", "-json", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s\n%s", err, stderr.String(), stdout.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatal(err)
	}
	want := report{Path: path, Harness: "antigravity", Entries: 4, ToolUses: 1, ToolResults: 1}
	if len(reports) != 1 || reports[0] != want {
		t.Fatalf("reports = %+v, want [%+v]", reports, want)
	}
}
