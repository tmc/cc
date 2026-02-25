package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func TestReadGeminiSessionJSON_ContentFormats(t *testing.T) {
	tests := []struct {
		name        string
		userContent string
		wantUser    string
	}{
		{
			name:        "legacy string",
			userContent: `"legacy prompt"`,
			wantUser:    "legacy prompt",
		},
		{
			name:        "modern array",
			userContent: `[{"text":"modern prompt"}]`,
			wantUser:    "modern prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "session-2026-02-23T10-00-abcd1234.json")
			body := `{
				"sessionId":"abcd1234",
				"messages":[
					{"id":"u1","timestamp":"2026-02-23T10:00:00Z","type":"user","content":` + tt.userContent + `},
					{"id":"a1","timestamp":"2026-02-23T10:00:05Z","type":"gemini","content":"answer","model":"gemini-2.5-pro","tokens":{"input":10,"output":2,"cached":4}}
				]
			}`
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("write session: %v", err)
			}

			entries, err := readSessionEntries(path)
			if err != nil {
				t.Fatalf("readSessionEntries: %v", err)
			}
			if len(entries) != 2 {
				t.Fatalf("entries len = %d, want 2", len(entries))
			}
			if got := entries[0].Message.TextContent(); got != tt.wantUser {
				t.Fatalf("user text = %q, want %q", got, tt.wantUser)
			}
			if got := entries[1].Message.TextContent(); got != "answer" {
				t.Fatalf("assistant text = %q, want %q", got, "answer")
			}
		})
	}
}

func TestBuildHandoffExtractsFilesAndCommands(t *testing.T) {
	assistantContent := mustJSONRaw(t, []any{
		map[string]any{
			"type": "tool_use",
			"name": "Write",
			"input": map[string]any{
				"file_path": "a.go",
			},
		},
		map[string]any{
			"type": "tool_use",
			"name": "Bash",
			"input": map[string]any{
				"command": "go test ./...",
			},
		},
		map[string]any{
			"type": "text",
			"text": "implemented the fix",
		},
	})

	entries := []cc.Entry{
		{
			Timestamp: time.Date(2026, 2, 23, 10, 0, 0, 0, time.UTC),
			Message: &cc.Message{
				Role:    "user",
				Content: json.RawMessage(`"please fix bug"`),
			},
		},
		{
			Timestamp: time.Date(2026, 2, 23, 10, 1, 0, 0, time.UTC),
			Message: &cc.Message{
				Role:    "assistant",
				Content: assistantContent,
			},
		},
	}

	sum := cc.Summarize("session.jsonl", entries)
	got := buildHandoff("session.jsonl", "claude-code", "gemini-cli", "gemini", entries, sum, 10, 10, 10)

	if got.LatestUserAsk != "please fix bug" {
		t.Fatalf("LatestUserAsk = %q, want %q", got.LatestUserAsk, "please fix bug")
	}
	if len(got.FilesTouched) != 1 || got.FilesTouched[0] != "a.go" {
		t.Fatalf("FilesTouched = %#v, want [a.go]", got.FilesTouched)
	}
	if len(got.RecentCommands) != 1 || got.RecentCommands[0] != "go test ./..." {
		t.Fatalf("RecentCommands = %#v, want [go test ./...]", got.RecentCommands)
	}
	prompt := renderPrompt(got)
	if !strings.Contains(prompt, "Likely touched files:") {
		t.Fatalf("renderPrompt missing touched files section")
	}
	if !strings.Contains(prompt, "Recent shell commands:") {
		t.Fatalf("renderPrompt missing shell commands section")
	}
}

func mustJSONRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

func TestNormalizeTargetCodex(t *testing.T) {
	tests := []struct {
		in         string
		wantTarget string
		wantBin    string
	}{
		{in: "codex", wantTarget: "codex-cli", wantBin: "codex"},
		{in: "codex-cli", wantTarget: "codex-cli", wantBin: "codex"},
		{in: "codex-app", wantTarget: "codex-cli", wantBin: "codex"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			gotTarget, gotBin, err := normalizeTarget(tt.in)
			if err != nil {
				t.Fatalf("normalizeTarget(%q): %v", tt.in, err)
			}
			if gotTarget != tt.wantTarget || gotBin != tt.wantBin {
				t.Fatalf("normalizeTarget(%q) = (%q, %q), want (%q, %q)", tt.in, gotTarget, gotBin, tt.wantTarget, tt.wantBin)
			}
		})
	}
}

func TestInferSourceAgentCodexMetadata(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		entries []cc.Entry
		want    string
	}{
		{
			name: "cli metadata",
			path: "/tmp/session.jsonl",
			entries: []cc.Entry{
				{Originator: "codex_cli_rs", Source: "cli"},
			},
			want: "codex-cli",
		},
		{
			name: "app metadata",
			path: "/tmp/session.jsonl",
			entries: []cc.Entry{
				{Originator: "Codex Desktop", Source: "vscode"},
			},
			want: "codex-app",
		},
		{
			name:    "path fallback",
			path:    filepath.Join("/tmp", ".codex", "sessions", "2026", "02", "25", "s.jsonl"),
			entries: nil,
			want:    "codex-cli",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferSourceAgent(tt.path, tt.entries)
			if got != tt.want {
				t.Fatalf("inferSourceAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}
