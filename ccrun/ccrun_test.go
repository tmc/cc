package ccrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tmc/cc/ccpaths"
)

func TestParseClaudeResult(t *testing.T) {
	const single = `{"type":"result","subtype":"success","is_error":false,"result":"Paris","session_id":"abc","num_turns":2}`
	const array = `[{"type":"system","subtype":"init","session_id":"s1"},` +
		`{"type":"assistant","message":{"role":"assistant"}},` +
		`{"type":"result","subtype":"success","is_error":false,"result":"London","session_id":"s1"}]`
	const errEnv = `{"type":"result","subtype":"error","is_error":true,"result":"Credit balance is too low"}`

	tests := []struct {
		name       string
		in         string
		wantResult string
		wantErr    bool
		parseErr   bool
	}{
		{name: "single object", in: single, wantResult: "Paris"},
		{name: "event array", in: array, wantResult: "London"},
		{name: "error envelope", in: errEnv, wantResult: "Credit balance is too low", wantErr: true},
		{name: "empty", in: "", parseErr: true},
		{name: "garbage", in: "not json at all", parseErr: true},
		{name: "empty array", in: "[]", parseErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseClaudeResult([]byte(tt.in))
			if tt.parseErr {
				if err == nil {
					t.Fatalf("want parse error, got result %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if res.Result != tt.wantResult {
				t.Errorf("Result = %q, want %q", res.Result, tt.wantResult)
			}
			if res.IsError != tt.wantErr {
				t.Errorf("IsError = %v, want %v", res.IsError, tt.wantErr)
			}
		})
	}
}

func TestRunWithFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary script is POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeclaude")
	script := "#!/bin/sh\n" +
		`printf '%s' '{"type":"result","subtype":"success","is_error":false,"result":"Answer: 42","session_id":"x"}'` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	workDir := filepath.Join(dir, "work")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_HOME", filepath.Join(dir, "claude-home"))
	writeTranscript(t, os.Getenv("CLAUDE_HOME"), workDir, "x")

	res, err := Run(context.Background(), "q", Options{
		Binary:         bin,
		WorkDir:        workDir,
		SystemPreamble: "PREAMBLE",
		SkillText:      "# Skill\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Output != "Answer: 42" {
		t.Errorf("Output = %q, want Answer: 42", res.Output)
	}
	if res.SessionID != "x" {
		t.Errorf("SessionID = %q, want x", res.SessionID)
	}
	if len(res.Transcript) != 2 {
		t.Fatalf("Transcript length = %d, want 2", len(res.Transcript))
	}
}

func TestRunSurfacesErrorEnvelope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary script is POSIX shell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "failclaude")
	script := "#!/bin/sh\n" +
		`printf '%s' '{"type":"result","is_error":true,"result":"nope","session_id":"x"}'` + "\n" +
		"exit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), "q", Options{Binary: bin, WorkDir: dir})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %q, want envelope message", err)
	}
	if res.Output != "nope" {
		t.Errorf("Output = %q, want nope", res.Output)
	}
}

func TestJoinSystem(t *testing.T) {
	tests := []struct {
		name     string
		preamble string
		skill    string
		want     string
	}{
		{name: "both", preamble: "P", skill: "S\n", want: "P\n\nS"},
		{name: "preamble", preamble: "P", want: "P"},
		{name: "skill", skill: "S\n", want: "S"},
		{name: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinSystem(tt.preamble, tt.skill); got != tt.want {
				t.Fatalf("joinSystem() = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeTranscript(t *testing.T, claudeHome, workDir, sessionID string) {
	t.Helper()
	abs, err := filepath.Abs(workDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claudeHome, "projects", ccpaths.EncodePath(abs), sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","sessionId":"x","message":{"role":"user","content":[{"type":"text","text":"q"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Answer: 42"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ExampleRun() {
	ctx := context.Background()
	dir, _ := os.MkdirTemp("", "ccrun-example")
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "fakeclaude")
	_ = os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '{\"type\":\"result\",\"result\":\"hello\"}'\n"), 0o755)

	res, _ := Run(ctx, "hello", Options{
		Binary:         bin,
		SystemPreamble: "answer briefly",
		SkillText:      "# Skill\n- Be precise.\n",
	})
	fmt.Println(res.Output)
	// Output: hello
}
