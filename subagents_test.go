package cc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSubagents(t *testing.T) {
	for _, tt := range []struct {
		name                      string
		child, meta, notification bool
		want                      int
	}{
		{"none", false, false, false, 0},
		{"child", true, false, false, 1},
		{"joined", true, true, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parent.jsonl")
			parent := `{"type":"user","sessionId":"p","message":{"role":"user","content":"hi"}}` + "\n"
			if tt.notification {
				parent += `{"type":"queue-operation","content":"<task-notification><task-id>a</task-id><tool-use-id>tool1</tool-use-id><status>completed</status></task-notification>"}` + "\n"
			}
			writeTestFile(t, path, parent)
			dir := filepath.Join(filepath.Dir(path), "parent", "subagents")
			if tt.child {
				writeTestFile(t, filepath.Join(dir, "agent-a.jsonl"), `{"type":"assistant","sessionId":"p","message":{"role":"assistant","content":"done"}}`+"\n")
			}
			if tt.meta {
				writeTestFile(t, filepath.Join(dir, "agent-a.meta.json"), `{"agentType":"Explore","description":"look"}`)
			}
			children, err := Subagents(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if len(children) != tt.want {
				t.Fatalf("children = %d, want %d", len(children), tt.want)
			}
			if tt.want == 0 {
				return
			}
			c := children[0]
			if c.AgentID != "a" || c.ParentSessionID != "p" || len(c.Entries) != 1 || !c.Entries[0].IsSidechain || c.Entries[0].AgentID != "a" {
				t.Fatalf("child = %+v", c)
			}
			if tt.notification && (c.ToolUseID != "tool1" || c.Notification == nil || c.Meta.AgentType != "Explore") {
				t.Fatalf("join = %+v", c)
			}
		})
	}
}

func TestCodexSubagents(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	meta := func(id, parent string) string {
		return `{"type":"session_meta","payload":{"id":"` + id + `","source":{"subagent":{"thread_spawn":{"parent_thread_id":"` + parent + `"}}}}}` + "\n"
	}
	parent := filepath.Join(root, "2026", "09", "parent.jsonl")
	writeTestFile(t, parent, meta("p", ""))
	writeTestFile(t, filepath.Join(root, "2026", "10", "child.jsonl"), meta("c", "p"))
	writeTestFile(t, filepath.Join(root, "other.jsonl"), meta("other", "unrelated"))
	writeTestFile(t, filepath.Join(root, "grandchild.jsonl"), meta("g", "c"))
	children, err := Subagents(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].AgentID != "c" || children[0].ParentSessionID != "p" || children[0].ToolUseID != "" {
		t.Fatalf("children = %+v", children)
	}
}

func TestSubagentErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		read func(context.Context, string) error
	}{
		{"linkage", func(ctx context.Context, p string) error { _, err := Subagents(ctx, p); return err }},
		{"entries", func(ctx context.Context, p string) error { _, err := ReadSubagents(ctx, p); return err }},
		{"merged", func(ctx context.Context, p string) error { _, err := ReadFileWithSubagents(ctx, p); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "p.jsonl")
			writeTestFile(t, path, "{\"type\":\"user\"}\n")
			// A dangling symlink reliably fails even when tests run as a privileged user.
			dir := filepath.Join(filepath.Dir(path), "p", "subagents")
			writeTestFile(t, filepath.Join(dir, "keep"), "")
			if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "agent-broken.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := tt.read(context.Background(), path); err == nil {
				t.Fatal("missing child error was hidden")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tt.read(ctx, path); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled = %v", err)
			}
		})
	}
}

func TestSubagentMetadataError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.jsonl")
	writeTestFile(t, path, "{\"type\":\"user\"}\n")
	dir := filepath.Join(filepath.Dir(path), "p", "subagents")
	writeTestFile(t, filepath.Join(dir, "agent-a.jsonl"), "{\"type\":\"assistant\"}\n")
	writeTestFile(t, filepath.Join(dir, "agent-a.meta.json"), "{")
	if _, err := Subagents(context.Background(), path); err == nil {
		t.Fatal("malformed metadata error hidden")
	}
}
