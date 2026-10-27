package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tmc/cc"
)

func TestSubagentDiscovery(t *testing.T) {
	for _, tt := range []struct {
		name, kind, path, data string
		child, supporting      bool
	}{
		{"claude parent", "claude", "project/parent.jsonl", `{"type":"user","sessionId":"parent","isSidechain":false}`, false, false},
		{"claude nested", "claude", "project/parent/subagents/agent-child.jsonl", `{"type":"user","sessionId":"parent","isSidechain":true}`, true, false},
		{"claude legacy", "claude", "project/agent-child.jsonl", `{"type":"user","sessionId":"parent","isSidechain":true}`, true, false},
		{"claude sidechain", "claude", "project/child.jsonl", `{"type":"user","sessionId":"child","isSidechain":true}`, true, false},
		{"claude workflow", "claude", "project/parent/subagents/workflows/run/events.jsonl", `{"type":"started"}`, false, true},
		{"codex parent", "codex", "2026/rollout-parent.jsonl", `{"type":"session_meta","payload":{"id":"parent","source":"cli"}}`, false, false},
		{"codex legacy", "codex", "2026/rollout-child.jsonl", `{"type":"session_meta","payload":{"id":"child","thread_source":"subagent"}}`, true, false},
		{"codex spawn", "codex", "2026/rollout-child.jsonl", `{"type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`, true, false},
		{"codex review", "codex", "2026/rollout-child.jsonl", `{"type":"session_meta","payload":{"id":"child","source":{"subagent":"review"}}}`, true, false},
		{"opencode parent", "opencode", "project/ses_parent.json", `{"id":"ses_parent"}`, false, false},
		{"opencode child", "opencode", "project/ses_child.json", `{"id":"ses_child","parentID":"ses_parent"}`, true, false},
		{"pi message parent", "pi", "project/session.jsonl", "{\"type\":\"session\",\"id\":\"parent\"}\n{\"type\":\"message\",\"parentId\":\"msg1\"}", false, false},
		{"gemini parent", "gemini", "project/parent.jsonl", `{"type":"user","sessionId":"parent"}`, false, false},
		{"antigravity parent", "antigravity", "conv/.system_generated/logs/transcript.jsonl", `{"step_index":0,"type":"USER_INPUT","content":"hello"}`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), tt.kind)
			switch tt.kind {
			case "claude":
				t.Setenv("CLAUDE_HOME", root)
				root = filepath.Join(root, "projects")
			case "codex":
				t.Setenv("CODEX_HOME", root)
				root = filepath.Join(root, "sessions")
			case "gemini":
				t.Setenv("GEMINI_HOME", root)
				root = filepath.Join(root, "projects")
			case "antigravity":
				root = filepath.Join(root, "brain")
			}
			path := filepath.Join(root, filepath.FromSlash(tt.path))
			writeTextFile(t, path, tt.data)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			old := *subagentsFlag
			t.Cleanup(func() { *subagentsFlag = old })
			for _, include := range []bool{false, true} {
				*subagentsFlag = include
				entry, ok := indexEntryForPath(path, searchRoot{dir: root, kind: tt.kind}, info)
				var matches []cc.IndexEntry
				if ok {
					matches = filterMatches([]cc.IndexEntry{entry}, "", "")
				}
				want := !tt.supporting && (!tt.child || include)
				if (len(matches) == 1) != want {
					t.Errorf("subagents=%v: matches = %v, want included=%v", include, matches, want)
				}
				if include && tt.kind == "claude" && tt.child && entry.SessionID == "agent-child" {
					bin, args := resumeInvocation(entry)
					if bin != "claude" || len(args) != 2 || args[1] != "parent" {
						t.Errorf("invocation = %s %v, want claude -r parent", bin, args)
					}
				}
			}
		})
	}
}

func TestFilterIndexedSidechain(t *testing.T) {
	old := *subagentsFlag
	t.Cleanup(func() { *subagentsFlag = old })
	for _, include := range []bool{false, true} {
		*subagentsFlag = include
		got := filterMatches([]cc.IndexEntry{{SessionID: "child", IsSidechain: true}}, "", "")
		if (len(got) == 1) != include {
			t.Fatalf("subagents=%v: got %v", include, got)
		}
	}
}
