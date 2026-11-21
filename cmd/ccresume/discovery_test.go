package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func TestDiscoveryRejectsSupportingFiles(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	for _, tt := range []struct {
		kind, path, data string
		want             bool
	}{
		{"claude", "needle/session.jsonl", `{"type":"user","text":"needle"}`, true},
		{"claude", "needle/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"claude", "needle/session/subagents/workflows/run/events.jsonl", `{"type":"started","text":"needle"}`, false},
		{"claude", "needle/agent-123.jsonl", `{"type":"user","text":"needle"}`, false},
		{"gemini", "needle/session.jsonl", `{"type":"user","text":"needle"}`, true},
		{"gemini", "needle/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"gemini", "needle/session/events.jsonl", `{"type":"started","text":"needle"}`, false},
		{"gemini-cli", "proj/chats/session-2026-04-03T22-56-d00a4143.json", `{"sessionId":"d00a4143","messages":[{"type":"user","content":"needle"}]}`, true},
		{"gemini-cli", "proj/logs.json", `{"text":"needle"}`, false},
		{"gemini-cli", "proj/chats/session-corrupt.json", `needle invalid json`, false},
		{"gemini-cli", "proj/chats/session-support.json", `{"text":"needle"}`, false},
		{"codex", "2026/rollout.jsonl", `{"type":"session_meta","payload":{"id":"needle","cwd":"/work"}}`, true},
		{"codex", "2026/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"codex", "2026/rollout.jsonl", `{"type":"session_meta","payload":{"cwd":"/needle"}}`, false},
		{"codex", "2026/rollout.jsonl", `{"type":"session_meta","payload":{"id":"needle","thread_source":"subagent"}}`, false},
		{"pi", "project/ts_id.jsonl", `{"type":"session","id":"needle","cwd":"/work"}`, true},
		{"pi", "project/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"pi", "project/ts_id.jsonl", `{"type":"session","cwd":"/needle"}`, false},
		{"opencode", "project/ses_123.json", `{"id":"ses_123","directory":"/needle"}`, true},
		{"opencode", "project/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"opencode", "project/ses_123.json", `{"directory":"/needle"}`, false},
		{"opencode", "project/ses_123.json", `needle invalid json`, false},
		{"antigravity", "conv/.system_generated/logs/transcript.jsonl", `{"step_index":0,"type":"USER_INPUT","content":"needle"}`, true},
		{"antigravity", "conv/.system_generated/logs/transcript_full.jsonl", `{"step_index":0,"type":"USER_INPUT","content":"needle"}`, true},
		{"antigravity", "conv/.system_generated/logs/journal.jsonl", `{"type":"started","text":"needle"}`, false},
		{"antigravity", "conv/.system_generated/logs/events.jsonl", `{"type":"started","text":"needle"}`, false},
		{"antigravity", "conv/.system_generated/logs/chunks/transcript.jsonl", `{"type":"started","text":"needle"}`, false},
	} {
		t.Run(tt.kind+"/"+tt.path, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "brain")
			path := filepath.Join(root, filepath.FromSlash(tt.path))
			writeTextFile(t, path, tt.data)
			roots := []searchRoot{{dir: root, kind: tt.kind}}
			lists, err := recentCandidates(roots, 60)
			if err != nil {
				t.Fatal(err)
			}
			content, err := contentMatches("needle", roots, lists)
			if err != nil {
				t.Fatal(err)
			}
			check := func(name string, got []cc.IndexEntry, want bool) {
				t.Helper()
				n := 0
				if want {
					n = 1
				}
				if len(got) != n {
					t.Errorf("%s = %#v, want %d entries", name, got, n)
				}
			}
			check("content", content, tt.want)
			meta := metadataMatches("needle", roots, lists)
			check("metadata", meta, tt.want && (tt.kind == "claude" || tt.kind == "gemini" || tt.kind == "codex"))
		})
	}
}

func TestAntigravityDirFileContainsAndCount(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	dir := filepath.Join(t.TempDir(), "brain", "4b0d0191-27af-4f4f-be0d-20ad2ea18a83")
	logsDir := filepath.Join(dir, ".system_generated", "logs")
	writeTextFile(t, filepath.Join(logsDir, "transcript.jsonl"), `{"step_index":0,"type":"USER_INPUT","content":"hello","truncated_fields":["content"]}`+"\n")
	writeTextFile(t, filepath.Join(logsDir, "transcript_full.jsonl"), `{"step_index":0,"type":"USER_INPUT","content":"nd encodes no NVIDIA hardware/ISA specific"}`+"\n")

	ok, err := fileContains(dir, "nd encodes no NVIDIA hardware/ISA specific")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Errorf("fileContains(%q) = false, want true", dir)
	}
	if n := countFileOccurrences("nd encodes no NVIDIA hardware/ISA specific", dir); n != 1 {
		t.Errorf("countFileOccurrences(%q) = %d, want 1", dir, n)
	}
}

func TestGeminiCLIDirectoryDiscovery(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	root := testSessionHomes(t)
	project := filepath.Join(root, "gemini-data", "tmp", "project")
	workspace := filepath.Join(root, "workspace")
	path := filepath.Join(project, "chats", "session-test.json")
	writeTextFile(t, filepath.Join(project, ".project_root"), workspace+"\n")
	writeTextFile(t, path, `{"sessionId":"gemini-chat","messages":[{"type":"user","content":"hello"}]}`)

	spec := searchSpec{query: workspace, dir: workspace, harness: "gemini"}
	got, err := spec.run(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].FullPath != path || got[0].SessionID != "gemini-chat" {
		t.Fatalf("search by workspace = %#v, want session gemini-chat at %q", got, path)
	}
}

func TestOpenCodeContentDiscovery(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	root := testSessionHomes(t)
	storage := filepath.Join(root, "opencode-data", "storage")
	path := filepath.Join(storage, "session", "project", "ses_content.json")
	now := time.Now().UnixMilli()
	writeTextFile(t, path, fmt.Sprintf(`{"id":"ses_content","time":{"created":%d,"updated":%d}}`, now, now))
	writeTextFile(t, filepath.Join(storage, "message", "ses_content", "first.json"), fmt.Sprintf(`{"id":"first","role":"user","time":{"created":%d}}`, now))
	writeTextFile(t, filepath.Join(storage, "message", "ses_content", "later.json"), fmt.Sprintf(`{"id":"later","role":"assistant","time":{"created":%d}}`, now+1))
	writeTextFile(t, filepath.Join(storage, "part", "first", "text.json"), `{"id":"p1","messageID":"first","sessionID":"ses_content","type":"text","text":"first question"}`)
	writeTextFile(t, filepath.Join(storage, "part", "later", "text.json"), `{"id":"p2","messageID":"later","sessionID":"ses_content","type":"text","text":"later answer"}`)

	for _, tt := range []struct {
		name, query, has string
	}{
		{"query", "later answer", ""},
		{"has alone", "later answer", "later answer"},
		{"has with ID", "ses_content", "later answer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := searchSpec{query: tt.query, has: tt.has, harness: "opencode"}
			got, err := spec.run(time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].FullPath != path {
				t.Fatalf("search = %#v, want path %q", got, path)
			}
		})
	}
}

func testSessionHomes(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude-data"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex-data"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini-data"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode-data"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi-data"))
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(root, "agy-data"))
	t.Setenv("NAC_HOME", filepath.Join(root, "nac-data"))
	t.Setenv("CC_NAC_STORE", "")
	return root
}
