package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tmc/cc"
)

func TestResolveMatchWorkspaceEvidence(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "tmc", "warren")
	start := filepath.Join(root, "realtime-voice-chat")
	for _, dir := range []string{project, start} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name, query, field, value string
		want                      string
	}{
		{"file tool", "tmc/warren", "Path", filepath.Join(project, "README.md"), project},
		{"case insensitive", "TMC/WARREN", "Path", filepath.Join(project, "README.md"), project},
		{"workdir", "tmc/warren", "workdir", project, project},
		{"absolute query", project, "file_path", filepath.Join(project, "doc.go"), project},
		{"prose", "tmc/warren", "text", project, start},
		{"command output", "tmc/warren", "output", project, start},
		{"partial component", "tmc/warr", "Path", filepath.Join(project, "README.md"), start},
		{"plain text query", "warren", "Path", filepath.Join(project, "README.md"), start},
		{"missing directory", "tmc/gone", "workdir", filepath.Join(root, "tmc", "gone"), start},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeJSONL(t, path, []map[string]any{{
				"type": "assistant", "sessionId": "s1", "cwd": start,
				"message": map[string]any{"role": "assistant", "content": []map[string]any{{
					"type": "tool_use", "name": "read_file", "input": map[string]string{tt.field: tt.value},
				}}},
			}})
			got := resolveMatch(tt.query, cc.IndexEntry{FullPath: path, ProjectPath: start})
			if got.target != tt.want {
				t.Fatalf("target = %q, want %q", got.target, tt.want)
			}
		})
	}

	// The actual Antigravity schema uses Path without recording a CWD.
	path := filepath.Join(root, "brain", "conv", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{"Path": filepath.Join(project, "README.md")})
	if err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, []map[string]any{{"step_index": 0, "type": "PLANNER_RESPONSE", "tool_calls": []map[string]any{{"name": "read_file", "args": json.RawMessage(input)}}}})
	got := resolveMatch("tmc/warren", cc.IndexEntry{FullPath: path, ProjectPath: filepath.Join(root, "antigravity", "brain", "conv")})
	if got.target != project {
		t.Fatalf("Antigravity target = %q, want %q", got.target, project)
	}

	// A resumed Codex session may have returned to its starting directory after
	// working in the queried project. Preserve the query's workspace.
	path = filepath.Join(root, ".codex", "sessions", "rollout.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, path, []map[string]any{
		{"type": "session_meta", "payload": map[string]string{"id": "s1", "cwd": start}},
		{"type": "turn_context", "payload": map[string]string{"cwd": project}},
		{"type": "turn_context", "payload": map[string]string{"cwd": start}},
	})
	got = resolveMatch("tmc/warren", cc.IndexEntry{FullPath: path, ProjectPath: start})
	if got.target != project {
		t.Fatalf("Codex target = %q, want %q", got.target, project)
	}
}
