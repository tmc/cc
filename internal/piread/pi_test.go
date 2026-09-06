package piread

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc/internal/ccmodel"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsPiSessionPath(t *testing.T) {
	// Fast-path cases that do not require a header sniff (agent+sessions
	// adjacency), plus negatives. Sniff-only cases (sessions present but no
	// agent parent) are covered by TestIsPiSessionPathSniff with real files.
	tests := []struct {
		path string
		want bool
	}{
		{"/home/u/.pi/agent/sessions/--p--/ts_id.jsonl", true},
		{"/custom/agent/sessions/proj/x.jsonl", true},
		{"/a/agent/sessions/proj/x.jsonl", true},           // shallow agent/sessions/<proj>/<file> (off-by-one regression).
		{"/home/u/.claude/projects/-enc/abc.jsonl", false}, // claude jsonl, no agent/sessions.
		{"/home/u/.pi/agent/sessions/--p--/x.json", false}, // not jsonl.
		{"/agent/sessions/x.jsonl", false},                 // file directly under sessions, no project dir.
	}
	for _, tt := range tests {
		if got := IsSessionPath(tt.path); got != tt.want {
			t.Errorf("IsSessionPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestIsPiSessionPathSniff(t *testing.T) {
	// A PI_CODING_AGENT_DIR override whose dir is not named "agent": detection
	// must fall back to sniffing the session header, including for shallow
	// sessions/ paths that the loop bound previously skipped.
	root := t.TempDir()
	piFile := filepath.Join(root, "sessions", "proj", "x.jsonl")
	writeTestFile(t, piFile, `{"type":"session","id":"s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/w"}`+"\n")
	if !IsSessionPath(piFile) {
		t.Errorf("IsSessionPath(%q) = false, want true (sniff)", piFile)
	}

	// A codex-style file under sessions/ whose first line is session_meta, not
	// session, must not be mistaken for pi.
	codexFile := filepath.Join(root, "sessions", "2026", "rollout.jsonl")
	writeTestFile(t, codexFile, `{"type":"session_meta","payload":{}}`+"\n")
	if IsSessionPath(codexFile) {
		t.Errorf("IsSessionPath(%q) = true, want false (codex session_meta)", codexFile)
	}
}

func TestReadFilePiErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", "pi session empty"},
		{"missing header", `{"type":"message","id":"a"}` + "\n", "pi session missing header"},
		{"not session type", `{"type":"model_change"}` + "\n", "pi session missing header"},
		{"missing id", `{"type":"session","timestamp":"2026-04-19T00:00:00.000Z"}` + "\n", "pi session missing id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTestFile(t, path, tt.content)
			_, err := ReadFile(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ReadFile err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestPiToolName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"bash", "Bash"},
		{"edit", "Edit"},
		{"write", "Write"},
		{"read", "Read"},
		{"grep", "Grep"},
		{"glob", "Glob"},
		{"find", "Glob"},
		{"ls", "LS"},
		{"GREP", "Grep"},     // case-insensitive.
		{"mytool", "mytool"}, // custom names pass through unchanged.
	}
	for _, tt := range tests {
		if got := piToolName(tt.in); got != tt.want {
			t.Errorf("piToolName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestReadFilePiUsageShapes checks that both serializations of a pi assistant
// message decode: the TypeScript agent writes lowercase usage keys and
// "toolCall" blocks, the Go agent marshals llm.Usage and llm.Block with their
// Go field and type names. Both appear on disk, and Go's case-insensitive JSON
// matching does not bridge "InputTokens" to `input`, so the Go shape used to
// read as an all-zero usage with its tool call dropped.
func TestReadFilePiUsageShapes(t *testing.T) {
	const header = `{"type":"session","id":"s1","timestamp":"2026-07-31T18:13:19.379Z","cwd":"/w"}`
	tests := []struct {
		name                string
		line                string
		wantIn              int
		wantOut             int
		wantRead, wantWrite int
	}{
		{
			name:   "typescript",
			line:   `{"type":"message","id":"m1","timestamp":"2026-07-31T18:14:53.959Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"t1","name":"bash","arguments":{}}],"model":"claude-opus-4-8","usage":{"input":2,"output":91,"cacheRead":10,"cacheWrite":3478}}}`,
			wantIn: 2, wantOut: 91, wantRead: 10, wantWrite: 3478,
		},
		{
			// A real go-pi line.
			name:   "go",
			line:   `{"type":"message","id":"m1","timestamp":"2026-07-31T18:14:53.959Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","arguments":{}}],"model":"claude-opus-4-8","usage":{"InputTokens":2,"OutputTokens":91,"CacheReadTokens":10,"CacheWriteTokens":3478}}}`,
			wantIn: 2, wantOut: 91, wantRead: 10, wantWrite: 3478,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTestFile(t, path, header+"\n"+tt.line+"\n")
			entries, err := ReadFile(context.Background(), path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			var msg *ccmodel.Message
			for _, e := range entries {
				if e.Message != nil && e.Message.Role == "assistant" {
					msg = e.Message
				}
			}
			if msg == nil {
				t.Fatalf("no assistant entry in %d entries", len(entries))
			}
			u := msg.Usage
			if u == nil {
				t.Fatal("assistant entry carries no usage")
			}
			if u.InputTokens != tt.wantIn || u.OutputTokens != tt.wantOut ||
				u.CacheReadInputTokens != tt.wantRead || u.CacheCreationInputTokens != tt.wantWrite {
				t.Errorf("usage = %+v; want in %d, out %d, read %d, write %d",
					u, tt.wantIn, tt.wantOut, tt.wantRead, tt.wantWrite)
			}
			blocks := msg.ContentBlocks()
			if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].Name != "Bash" {
				t.Errorf("blocks = %+v; want one tool_use named Bash", blocks)
			}
		})
	}
}
