package main

import (
	"os/exec"
	"path/filepath"
	"testing"

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
