package cc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSubagents_NoDir(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeJSONL(t, path, map[string]any{
		"type":      "user",
		"timestamp": "2026-04-20T10:00:00Z",
		"uuid":      "u1",
	})

	subs, err := ReadSubagents(path)
	if err != nil {
		t.Fatalf("ReadSubagents: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected 0 subagent entries, got %d", len(subs))
	}
}

func TestReadSubagents_TagsAgentIDAndSidechain(t *testing.T) {
	tmp := t.TempDir()
	parentPath := filepath.Join(tmp, "session.jsonl")
	writeJSONL(t, parentPath, map[string]any{
		"type":      "user",
		"timestamp": "2026-04-20T10:00:00Z",
		"uuid":      "parent-1",
	})

	subDir := filepath.Join(tmp, "session", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	subPath := filepath.Join(subDir, "agent-abc123.jsonl")
	writeJSONL(t, subPath,
		map[string]any{
			"type":      "user",
			"timestamp": "2026-04-20T10:00:01Z",
			"uuid":      "sub-1",
		},
		map[string]any{
			"type":      "assistant",
			"timestamp": "2026-04-20T10:00:02Z",
			"uuid":      "sub-2",
		},
	)

	subs, err := ReadSubagents(parentPath)
	if err != nil {
		t.Fatalf("ReadSubagents: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subagent entries, got %d", len(subs))
	}
	for _, e := range subs {
		if e.AgentID != "abc123" {
			t.Errorf("AgentID = %q, want %q", e.AgentID, "abc123")
		}
		if !e.IsSidechain {
			t.Errorf("IsSidechain = false, want true")
		}
	}
}

func TestReadSubagents_SkipsACompact(t *testing.T) {
	tmp := t.TempDir()
	parentPath := filepath.Join(tmp, "session.jsonl")
	writeJSONL(t, parentPath, map[string]any{
		"type":      "user",
		"timestamp": "2026-04-20T10:00:00Z",
	})

	subDir := filepath.Join(tmp, "session", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeJSONL(t, filepath.Join(subDir, "agent-acompact-xyz.jsonl"),
		map[string]any{"type": "user", "timestamp": "2026-04-20T10:00:01Z"},
	)
	writeJSONL(t, filepath.Join(subDir, "agent-real.jsonl"),
		map[string]any{"type": "user", "timestamp": "2026-04-20T10:00:02Z"},
	)

	subs, err := ReadSubagents(parentPath)
	if err != nil {
		t.Fatalf("ReadSubagents: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected 1 subagent entry (acompact skipped), got %d", len(subs))
	}
	if subs[0].AgentID != "real" {
		t.Errorf("AgentID = %q, want %q", subs[0].AgentID, "real")
	}
}

func TestReadFileWithSubagents_MergesAndSorts(t *testing.T) {
	tmp := t.TempDir()
	parentPath := filepath.Join(tmp, "session.jsonl")
	writeJSONL(t, parentPath,
		map[string]any{
			"type":      "user",
			"timestamp": "2026-04-20T10:00:00Z",
			"uuid":      "p1",
		},
		map[string]any{
			"type":      "assistant",
			"timestamp": "2026-04-20T10:00:10Z",
			"uuid":      "p2",
		},
	)

	subDir := filepath.Join(tmp, "session", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeJSONL(t, filepath.Join(subDir, "agent-sub1.jsonl"),
		map[string]any{
			"type":      "user",
			"timestamp": "2026-04-20T10:00:05Z",
			"uuid":      "s1",
		},
	)

	entries, err := ReadFileWithSubagents(parentPath)
	if err != nil {
		t.Fatalf("ReadFileWithSubagents: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	wantOrder := []string{"p1", "s1", "p2"}
	for i, w := range wantOrder {
		if entries[i].UUID != w {
			t.Errorf("entries[%d].UUID = %q, want %q", i, entries[i].UUID, w)
		}
	}
	if !entries[1].IsSidechain || entries[1].AgentID != "sub1" {
		t.Errorf("middle entry not tagged: IsSidechain=%v AgentID=%q", entries[1].IsSidechain, entries[1].AgentID)
	}
}
