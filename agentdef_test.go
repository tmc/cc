package cc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAndListAgentDefs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CC_AGENTS_DIR", dir)

	if err := os.MkdirAll(filepath.Join(dir, ".disabled"), 0o755); err != nil {
		t.Fatal(err)
	}

	active := `{
		"name": "sample",
		"description": "demo agent",
		"triggers": {"keywords": ["hi"]},
		"tools": ["Bash"],
		"capabilities": ["c1"]
	}`
	if err := os.WriteFile(filepath.Join(dir, "sample.json"), []byte(active), 0o644); err != nil {
		t.Fatal(err)
	}
	disabled := `{"name":"legacy","description":"old"}`
	if err := os.WriteFile(filepath.Join(dir, ".disabled", "legacy.json"), []byte(disabled), 0o644); err != nil {
		t.Fatal(err)
	}

	defs, err := ListAgentDefs()
	if err != nil {
		t.Fatalf("ListAgentDefs: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("len=%d want 2: %+v", len(defs), defs)
	}
	byName := map[string]*AgentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if got := byName["sample"]; got == nil || got.Disabled || len(got.Triggers.Keywords) != 1 {
		t.Errorf("sample def wrong: %+v", got)
	}
	if got := byName["legacy"]; got == nil || !got.Disabled {
		t.Errorf("legacy should be disabled: %+v", got)
	}
}
