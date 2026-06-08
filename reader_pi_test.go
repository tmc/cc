package cc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// piSession returns a representative pi JSONL session exercising the header,
// control entries (model_change, thinking_level_change), a user message, an
// assistant message with thinking/text/toolCall blocks, and a separate
// toolResult message.
const piSession = `{"type":"session","version":3,"id":"019da713-57ed-75bf-acd1-077bdb8399ad","timestamp":"2026-04-19T18:49:16.013Z","cwd":"/work/arpi"}
{"type":"model_change","id":"ac8c5212","parentId":null,"timestamp":"2026-04-19T18:49:16.092Z","provider":"anthropic","modelId":"claude-sonnet-4"}
{"type":"thinking_level_change","id":"ec7107b2","parentId":"ac8c5212","timestamp":"2026-04-19T18:49:16.092Z","thinkingLevel":"medium"}
{"type":"message","id":"ddb13842","parentId":"ec7107b2","timestamp":"2026-04-19T18:49:31.812Z","message":{"role":"user","content":[{"type":"text","text":"read main.go"}],"timestamp":1776624571810}}
{"type":"message","id":"9dba4314","parentId":"ddb13842","timestamp":"2026-04-19T18:49:42.186Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I should read the file."},{"type":"text","text":"Reading it now."},{"type":"toolCall","id":"call_01","name":"read","arguments":{"path":"main.go"}}],"api":"anthropic-messages","provider":"anthropic","model":"claude-sonnet-4","usage":{"input":100,"output":20,"cacheRead":5,"cacheWrite":2,"totalTokens":120},"stopReason":"toolUse","timestamp":1776624571835}}
{"type":"message","id":"b2c3d4e5","parentId":"9dba4314","timestamp":"2026-04-19T18:50:01.000Z","message":{"role":"toolResult","toolCallId":"call_01","toolName":"read","content":[{"type":"text","text":"package main"}],"isError":false,"timestamp":1776624601000}}
`

func TestReadFilePi(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "--work-arpi--", "2026-04-19T18-49-16-013Z_019da713.jsonl")
	writeTestFile(t, sessionPath, piSession)

	if !isPiSessionPath(sessionPath) {
		t.Fatalf("isPiSessionPath(%q) = false", sessionPath)
	}

	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// session_meta + user + assistant + toolResult (control entries skipped).
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %#v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Source != "pi" {
			t.Errorf("entry %q Source = %q, want pi", e.Type, e.Source)
		}
		if e.SessionID != "019da713-57ed-75bf-acd1-077bdb8399ad" {
			t.Errorf("entry %q SessionID = %q", e.Type, e.SessionID)
		}
	}

	meta := entries[0]
	if meta.Type != "session_meta" || meta.CWD != "/work/arpi" || meta.Version != "3" {
		t.Fatalf("meta = %#v", meta)
	}

	sum := Summarize(sessionPath, entries)
	if sum.SessionID != "019da713-57ed-75bf-acd1-077bdb8399ad" {
		t.Errorf("SessionID = %q", sum.SessionID)
	}
	if sum.CWD != "/work/arpi" {
		t.Errorf("CWD = %q, want /work/arpi", sum.CWD)
	}
	if sum.FirstPrompt != "read main.go" {
		t.Errorf("FirstPrompt = %q, want read main.go", sum.FirstPrompt)
	}
	if sum.Model != "claude-sonnet-4" {
		t.Errorf("Model = %q, want claude-sonnet-4", sum.Model)
	}
	// The toolResult message is a tool-result-only user message; it must not be
	// counted as a user turn.
	if sum.UserMessages != 1 || sum.AsstMessages != 1 || sum.ToolUses != 1 {
		t.Errorf("counts = user:%d asst:%d tools:%d", sum.UserMessages, sum.AsstMessages, sum.ToolUses)
	}

	assistant := entries[2]
	if assistant.Type != "assistant" {
		t.Fatalf("entries[2] = %q, want assistant", assistant.Type)
	}
	if assistant.Usage == nil || assistant.Usage.InputTokens != 100 || assistant.Usage.OutputTokens != 20 ||
		assistant.Usage.CacheReadInputTokens != 5 || assistant.Usage.CacheCreationInputTokens != 2 {
		t.Errorf("assistant usage = %#v", assistant.Usage)
	}
	// thinking is folded in as a text block alongside the assistant's text.
	var texts []string
	for _, b := range assistant.Message.ContentBlocks() {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) != 2 || texts[0] != "I should read the file." || texts[1] != "Reading it now." {
		t.Errorf("assistant text blocks = %#v, want thinking then text", texts)
	}
	uses := assistant.Message.ToolUses()
	if len(uses) != 1 || uses[0].Name != "Read" || uses[0].ID != "call_01" {
		t.Fatalf("tool uses = %#v, want one Read/call_01", uses)
	}

	toolResult := entries[3]
	if toolResult.Type != "user" {
		t.Fatalf("entries[3] = %q, want user (normalized toolResult)", toolResult.Type)
	}
	if !toolResult.Message.IsToolResultOnly() {
		t.Fatalf("toolResult entry not tool-result-only: %#v", toolResult)
	}
	results := toolResult.Message.ToolResults()
	if len(results) != 1 || results[0].ToolUseID != "call_01" || results[0].Content != "package main" || results[0].IsError {
		t.Fatalf("tool results = %#v", results)
	}
}

func TestReadFilePiToolErrorResult(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "proj", "sess.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"sid-err","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/work/cc"}
{"type":"message","id":"a1","parentId":null,"timestamp":"2026-04-19T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"false"}}],"model":"m","timestamp":1}}
{"type":"message","id":"a2","parentId":"a1","timestamp":"2026-04-19T00:00:02.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":"exit status 1","isError":true,"timestamp":2}}
`)

	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	result := entries[len(entries)-1]
	results := result.Message.ToolResults()
	if len(results) != 1 || !results[0].IsError || results[0].Content != "exit status 1" || results[0].ToolUseID != "c1" {
		t.Fatalf("tool results = %#v, want error result for c1", results)
	}
}

func TestReadFilePiBareStringContent(t *testing.T) {
	// User messages may carry content as a bare JSON string rather than blocks.
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "proj", "s.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"s","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"u","parentId":null,"timestamp":"2026-04-19T00:00:01.000Z","message":{"role":"user","content":"plain text prompt","timestamp":1}}
`)
	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := Summarize(sessionPath, entries).FirstPrompt; got != "plain text prompt" {
		t.Fatalf("FirstPrompt = %q, want plain text prompt", got)
	}
}

func TestReadFilePiImageBlock(t *testing.T) {
	// An assistant message may carry an image block (base64 data + mime type),
	// which maps to a canonical image ContentBlock alongside any text.
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "proj", "s.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"s","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a","parentId":null,"timestamp":"2026-04-19T00:00:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"here is a chart"},{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}],"model":"m","timestamp":1}}
`)
	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	blocks := entries[len(entries)-1].Message.ContentBlocks()
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want text+image: %#v", len(blocks), blocks)
	}
	if blocks[0].Type != "text" || blocks[0].Text != "here is a chart" {
		t.Fatalf("blocks[0] = %#v, want text \"here is a chart\"", blocks[0])
	}
	img := blocks[1]
	if img.Type != "image" || img.Data != "aGVsbG8=" || img.MIMEType != "image/png" {
		t.Fatalf("image block = %#v", img)
	}
}

func TestReadFilePiDropsEmptyToolResult(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "proj", "s.jsonl")
	// A toolResult with no id, no content, and no error carries nothing and
	// must not produce an entry; an erroring one with empty content must.
	writeTestFile(t, sessionPath, `{"type":"session","id":"s","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"e1","parentId":null,"timestamp":"2026-04-19T00:00:01.000Z","message":{"role":"toolResult","content":"","timestamp":1}}
{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-04-19T00:00:02.000Z","message":{"role":"toolResult","toolCallId":"c2","content":"","isError":true,"timestamp":2}}
`)
	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	// session_meta + the erroring tool result only.
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (empty tool result dropped): %#v", len(entries), entries)
	}
	results := entries[1].Message.ToolResults()
	if len(results) != 1 || !results[0].IsError || results[0].ToolUseID != "c2" {
		t.Fatalf("kept result = %#v, want erroring c2", results)
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
			_, err := readPiFile(context.Background(), path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("readPiFile err = %v, want containing %q", err, tt.want)
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

func TestReadFilePiBareStringThinking(t *testing.T) {
	// Two paths that the block-array tests miss: an assistant message whose
	// content is a bare string (not a block array), and a toolResult whose
	// content is a block array carrying a thinking block (exercising
	// piContentText's array+thinking branch rather than piBlocks).
	root := t.TempDir()
	sessionPath := filepath.Join(root, ".pi", "agent", "sessions", "proj", "s.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"s","timestamp":"2026-04-19T00:00:00.000Z","cwd":"/w"}
{"type":"message","id":"a","parentId":null,"timestamp":"2026-04-19T00:00:01.000Z","message":{"role":"assistant","content":"bare assistant reply","model":"m","timestamp":1}}
{"type":"message","id":"b","parentId":"a","timestamp":"2026-04-19T00:00:02.000Z","message":{"role":"toolResult","toolCallId":"c1","content":[{"type":"thinking","thinking":"pondering"},{"type":"text","text":"the answer"}],"timestamp":2}}
`)
	entries, err := ReadFile(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries[1].Message.TextContent(); got != "bare assistant reply" {
		t.Errorf("assistant bare-string text = %q, want %q", got, "bare assistant reply")
	}
	results := entries[2].Message.ToolResults()
	if len(results) != 1 || results[0].Content != "pondering\nthe answer" {
		t.Fatalf("tool result content = %#v, want thinking+text joined", results)
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
		if got := isPiSessionPath(tt.path); got != tt.want {
			t.Errorf("isPiSessionPath(%q) = %v, want %v", tt.path, got, tt.want)
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
	if !isPiSessionPath(piFile) {
		t.Errorf("isPiSessionPath(%q) = false, want true (sniff)", piFile)
	}

	// A codex-style file under sessions/ whose first line is session_meta, not
	// session, must not be mistaken for pi.
	codexFile := filepath.Join(root, "sessions", "2026", "rollout.jsonl")
	writeTestFile(t, codexFile, `{"type":"session_meta","payload":{}}`+"\n")
	if isPiSessionPath(codexFile) {
		t.Errorf("isPiSessionPath(%q) = true, want false (codex session_meta)", codexFile)
	}
}

func TestFindSessionFilesPi(t *testing.T) {
	root := t.TempDir()
	piHome := filepath.Join(root, "pi")
	sessionPath := filepath.Join(piHome, "sessions", "--work-find--", "ts_id.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"sid","timestamp":"2030-01-01T00:00:00.000Z","cwd":"/work/find"}
{"type":"message","id":"u","parentId":null,"timestamp":"2030-01-01T00:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"hi"}],"timestamp":1}}
`)
	if err := os.Chtimes(sessionPath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode"))
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	if err := os.MkdirAll(filepath.Join(root, "claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Match by cwd recorded in the session header (the encoded dir is lossy).
	got, err := FindSessionFiles(context.Background(), 24*time.Hour, "find")
	if err != nil {
		t.Fatalf("FindSessionFiles: %v", err)
	}
	if len(got) != 1 || got[0] != sessionPath {
		t.Fatalf("FindSessionFiles = %v, want [%s]", got, sessionPath)
	}
}

func TestPiIndexEntries(t *testing.T) {
	root := t.TempDir()
	piHome := filepath.Join(root, "pi")
	sessionPath := filepath.Join(piHome, "sessions", "--work-idx--", "ts_id.jsonl")
	writeTestFile(t, sessionPath, `{"type":"session","id":"idx-sid","timestamp":"2030-01-01T00:00:00.000Z","cwd":"/work/idx"}
{"type":"message","id":"u","parentId":null,"timestamp":"2030-01-01T00:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"first prompt here"}],"timestamp":1}}
`)
	if err := os.Chtimes(sessionPath, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_HOME", filepath.Join(root, "claude"))
	t.Setenv("GEMINI_HOME", filepath.Join(root, "gemini"))
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode"))
	t.Setenv("PI_CODING_AGENT_DIR", piHome)
	if err := os.MkdirAll(filepath.Join(root, "claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}

	all, err := AllIndexEntries(24*time.Hour, "idx")
	if err != nil {
		t.Fatalf("AllIndexEntries: %v", err)
	}
	var found *IndexEntry
	for i := range all {
		if all[i].FullPath == sessionPath {
			found = &all[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("pi session not in index: %#v", all)
	}
	if found.SessionID != "idx-sid" || found.ProjectPath != "/work/idx" || found.FirstPrompt != "first prompt here" {
		t.Fatalf("index entry = %#v", *found)
	}
}
