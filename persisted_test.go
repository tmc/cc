package cc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvePersistedOutput checks that reading a session restores tool output
// Claude Code spilled to a sidecar file, and that a missing sidecar leaves the
// inline preview in place rather than failing the read.
func TestResolvePersistedOutput(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "b3qnv3pyb.txt")
	full := "line one\nline two\nline three\n"
	if err := os.WriteFile(sidecar, []byte(full), 0o644); err != nil {
		t.Fatal(err)
	}
	preview := "<persisted-output>\nOutput too large (39.2KB). Full output saved to: " +
		sidecar + "\n\nPreview (first 2KB):\nline one\n"

	line := func(path string) string {
		e := map[string]any{
			"type": "user",
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": preview}},
			},
			"toolUseResult": map[string]any{
				"stdout":              "line one\nline tw", // capped mid-token
				"persistedOutputPath": path,
				"persistedOutputSize": len(full),
			},
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"resolved", sidecar, full},
		{"missing sidecar keeps preview", filepath.Join(dir, "pruned.txt"), "line one\nline tw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := ReadAll(context.Background(), strings.NewReader(line(tt.path)))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("read %d entries, want 1", len(entries))
			}
			tur := entries[0].ToolUseResult
			if tur == nil {
				t.Fatal("toolUseResult is nil")
			}
			if tur.Stdout != tt.want {
				t.Errorf("Stdout = %q, want %q", tur.Stdout, tt.want)
			}
			if tur.PersistedOutputSize != len(full) {
				t.Errorf("PersistedOutputSize = %d, want %d", tur.PersistedOutputSize, len(full))
			}
		})
	}
}
