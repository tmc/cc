package ccdistill

import (
	"encoding/json"
	"testing"

	"github.com/tmc/cc"
)

// Test fixture helpers. These build cc.Entry literals directly so the tests do
// not depend on real session files on disk. Message.Content is either a JSON
// string (a human prompt) or a JSON array of content blocks (assistant output
// or tool-result feedback), matching real-session shape.

// userText builds a human user entry whose content is a plain JSON string.
func userText(text string) cc.Entry {
	return cc.Entry{
		Type:    "user",
		Message: &cc.Message{Role: "user", Content: jsonString(text)},
	}
}

// assistant builds an assistant entry from raw content blocks.
func assistant(model string, blocks ...cc.ContentBlock) cc.Entry {
	return cc.Entry{
		Type:    "assistant",
		Message: &cc.Message{Role: "assistant", Model: model, Content: jsonBlocks(blocks)},
	}
}

// toolResultUser builds a user entry carrying only tool_result blocks (the
// agent feedback turn, IsToolResultOnly()==true).
func toolResultUser(blocks ...cc.ContentBlock) cc.Entry {
	return cc.Entry{
		Type:    "user",
		Message: &cc.Message{Role: "user", Content: jsonBlocks(blocks)},
	}
}

func textBlock(text string) cc.ContentBlock {
	return cc.ContentBlock{Type: "text", Text: text}
}

func toolUseBlock(id, name, input string) cc.ContentBlock {
	return cc.ContentBlock{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}
}

func toolResultBlock(toolUseID, content string, isErr bool) cc.ContentBlock {
	return cc.ContentBlock{Type: "tool_result", ToolUseID: toolUseID, Content: content, IsError: isErr}
}

func jsonString(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}

func jsonBlocks(blocks []cc.ContentBlock) json.RawMessage {
	b, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	return b
}

// TestTransformTrajectory exercises the canonical four-turn trajectory:
// user prompt -> assistant text+tool_use -> user tool_result -> assistant text.
func TestTransformTrajectory(t *testing.T) {
	entries := []cc.Entry{
		{
			Type:      "user",
			SessionID: "sess-1",
			CWD:       "/Users/x/app",
			GitBranch: "main",
			Message:   &cc.Message{Role: "user", Content: jsonString("add a -v flag to the CLI")},
		},
		assistant("claude-opus-4",
			textBlock("I'll read main.go first."),
			toolUseBlock("toolu_01", "Read", `{"file_path":"main.go"}`),
		),
		toolResultUser(toolResultBlock("toolu_01", "package main\n...", false)),
		assistant("claude-opus-4", textBlock("Done. Added a -v flag.")),
	}

	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if s == nil {
		t.Fatal("Transform returned nil sample for a real trajectory")
	}

	if got, want := len(s.Turns), 4; got != want {
		t.Fatalf("turn count = %d, want %d", got, want)
	}

	wantRoles := []string{RoleUser, RoleAssistant, RoleUser, RoleAssistant}
	for i, want := range wantRoles {
		if got := s.Turns[i].Role; got != want {
			t.Errorf("turn[%d].Role = %q, want %q", i, got, want)
		}
	}

	// The assistant turn must keep both its text and the tool_use block.
	asst := s.Turns[1]
	if got, want := len(asst.Blocks), 2; got != want {
		t.Fatalf("assistant turn blocks = %d, want %d", got, want)
	}
	if asst.Blocks[0].Kind != KindText {
		t.Errorf("assistant block[0].Kind = %q, want %q", asst.Blocks[0].Kind, KindText)
	}
	tu := asst.Blocks[1]
	if tu.Kind != KindToolUse {
		t.Fatalf("assistant block[1].Kind = %q, want %q", tu.Kind, KindToolUse)
	}
	if tu.ToolUse == nil {
		t.Fatal("tool_use block has nil ToolUse")
	}
	if tu.ToolUse.ID != "toolu_01" || tu.ToolUse.Name != "Read" {
		t.Errorf("tool_use = %+v, want id=toolu_01 name=Read", tu.ToolUse)
	}

	// The following user turn must carry the matching tool_result.
	tr := s.Turns[2]
	if got, want := len(tr.Blocks), 1; got != want {
		t.Fatalf("tool_result turn blocks = %d, want %d", got, want)
	}
	if tr.Blocks[0].Kind != KindToolResult || tr.Blocks[0].ToolRes == nil {
		t.Fatalf("turn[2] block[0] is not a tool_result: %+v", tr.Blocks[0])
	}
	if got := tr.Blocks[0].ToolRes.ToolUseID; got != "toolu_01" {
		t.Errorf("tool_result.ToolUseID = %q, want toolu_01", got)
	}

	// Meta should be derived from the entries.
	if s.Meta.SessionID != "sess-1" {
		t.Errorf("Meta.SessionID = %q, want sess-1", s.Meta.SessionID)
	}
	if s.Meta.Model != "claude-opus-4" {
		t.Errorf("Meta.Model = %q, want claude-opus-4", s.Meta.Model)
	}
	if s.Meta.GitBranch != "main" {
		t.Errorf("Meta.GitBranch = %q, want main", s.Meta.GitBranch)
	}
	if s.Meta.FirstPrompt != "add a -v flag to the CLI" {
		t.Errorf("Meta.FirstPrompt = %q", s.Meta.FirstPrompt)
	}
	if s.Meta.NumTurns != 4 {
		t.Errorf("Meta.NumTurns = %d, want 4", s.Meta.NumTurns)
	}
	if s.Meta.NumToolUses != 1 {
		t.Errorf("Meta.NumToolUses = %d, want 1", s.Meta.NumToolUses)
	}
}

// TestTransformCollapse verifies that assistant text and a tool_use arriving as
// two separate consecutive assistant entries merge into one assistant turn.
func TestTransformCollapse(t *testing.T) {
	entries := []cc.Entry{
		userText("do the thing"),
		assistant("m", textBlock("thinking out loud")),
		assistant("m", toolUseBlock("t1", "Bash", `{"command":"ls"}`)),
		toolResultUser(toolResultBlock("t1", "a\nb", false)),
		toolResultUser(toolResultBlock("t1", "more", false)),
	}

	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if s == nil {
		t.Fatal("nil sample")
	}

	// user, assistant(text+tool_use merged), user(two tool_results merged) = 3.
	if got, want := len(s.Turns), 3; got != want {
		t.Fatalf("turn count = %d, want %d (turns: %+v)", got, want, s.Turns)
	}
	if s.Turns[1].Role != RoleAssistant || len(s.Turns[1].Blocks) != 2 {
		t.Errorf("merged assistant turn = %+v", s.Turns[1])
	}
	if s.Turns[2].Role != RoleUser || len(s.Turns[2].Blocks) != 2 {
		t.Errorf("merged tool_result turn = %+v", s.Turns[2])
	}
}

// TestTransformFilteringAndSkips covers entry-level skipping and the
// SignalFilter min-tool-uses drop. It is table-driven over whole sessions.
func TestTransformFilteringAndSkips(t *testing.T) {
	metaUser := cc.Entry{
		Type:    "user",
		IsMeta:  true,
		Message: &cc.Message{Role: "user", Content: jsonString("noise from the CLI")},
	}
	commandUser := userText("<command-name>/clear</command-name>")
	systemEntry := cc.Entry{Type: "system"} // Message == nil, skipped.

	tests := []struct {
		name        string
		entries     []cc.Entry
		opts        Options
		wantNil     bool
		wantTurns   int
		minToolUses int
		wantKept    bool // SignalFilter result when sample is non-nil
		wantFirst   string
	}{
		{
			name: "no tool uses dropped by min-tool-uses",
			entries: []cc.Entry{
				userText("just chatting"),
				assistant("m", textBlock("hello back")),
			},
			minToolUses: 1,
			wantTurns:   2,
			wantKept:    false,
			wantFirst:   "just chatting",
		},
		{
			name: "tool-free chat kept when min-tool-uses is zero",
			entries: []cc.Entry{
				userText("just chatting"),
				assistant("m", textBlock("hello back")),
			},
			minToolUses: 0,
			wantTurns:   2,
			wantKept:    true,
			wantFirst:   "just chatting",
		},
		{
			name: "one tool use kept",
			entries: []cc.Entry{
				userText("run ls"),
				assistant("m", toolUseBlock("t1", "Bash", `{"command":"ls"}`)),
				toolResultUser(toolResultBlock("t1", "out", false)),
				assistant("m", textBlock("done")),
			},
			minToolUses: 1,
			wantTurns:   4,
			wantKept:    true,
			wantFirst:   "run ls",
		},
		{
			name: "IsMeta and command expansion skipped, leaving a clean turn",
			entries: []cc.Entry{
				metaUser,
				commandUser,
				userText("real prompt"),
				assistant("m", toolUseBlock("t1", "Read", `{}`)),
				toolResultUser(toolResultBlock("t1", "data", false)),
			},
			opts:        Options{DropCommands: true},
			minToolUses: 1,
			// metaUser dropped (IsMeta), commandUser dropped (DropCommands);
			// real prompt, assistant tool_use, tool_result = 3 turns.
			wantTurns: 3,
			wantKept:  true,
			wantFirst: "real prompt",
		},
		{
			name: "system and meta only yields nil sample",
			entries: []cc.Entry{
				systemEntry,
				metaUser,
			},
			wantNil: true,
		},
		{
			name:    "empty session yields nil sample",
			entries: nil,
			wantNil: true,
		},
		{
			name: "command expansion kept when DropCommands is false",
			entries: []cc.Entry{
				userText("<command-name>/help</command-name>"),
				assistant("m", textBlock("ok")),
			},
			opts:        Options{DropCommands: false},
			minToolUses: 0,
			wantTurns:   2,
			wantKept:    true,
			wantFirst:   "<command-name>/help</command-name>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Transform(tt.entries, tt.opts)
			if err != nil {
				t.Fatalf("Transform: %v", err)
			}
			if tt.wantNil {
				if s != nil {
					t.Fatalf("want nil sample, got %+v", s)
				}
				return
			}
			if s == nil {
				t.Fatal("got nil sample, want non-nil")
			}
			if got := len(s.Turns); got != tt.wantTurns {
				t.Errorf("turn count = %d, want %d (turns: %+v)", got, tt.wantTurns, s.Turns)
			}
			if got := s.Meta.FirstPrompt; got != tt.wantFirst {
				t.Errorf("FirstPrompt = %q, want %q", got, tt.wantFirst)
			}
			if got := SignalFilter(tt.minToolUses)(s); got != tt.wantKept {
				t.Errorf("SignalFilter(%d) = %v, want %v", tt.minToolUses, got, tt.wantKept)
			}
		})
	}
}

// TestSignalFilterNoHuman verifies a session with only tool-result user turns
// (no human prompt) is dropped: SignalFilter requires at least one human turn.
func TestSignalFilterNoHuman(t *testing.T) {
	entries := []cc.Entry{
		assistant("m", toolUseBlock("t1", "Read", `{}`)),
		toolResultUser(toolResultBlock("t1", "data", false)),
		assistant("m", textBlock("done")),
	}
	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if s == nil {
		t.Fatal("nil sample; an assistant turn should survive")
	}
	if SignalFilter(1)(s) {
		t.Error("SignalFilter kept a sample with no human user turn")
	}
	if got := FingerprintFirstUser(s); got != "" {
		t.Errorf("FingerprintFirstUser = %q, want empty (no human turn)", got)
	}
}

// TestTransformNoiseFilter is a table over the leading-tag drop behavior. With
// DropCommands set (the CLI default), a user turn whose text begins with a known
// harness wrapper (slash-command expansion or instruction/context preamble) is
// dropped, while a genuine prompt — including one that merely mentions a tag
// mid-sentence — is kept. With DropCommands unset (the library zero value), the
// noise turn survives. Each session pairs the candidate user turn with an
// assistant reply so a kept turn yields a sample and a dropped one yields the
// remaining assistant-only sample (still non-nil, but with no leading prompt).
func TestTransformNoiseFilter(t *testing.T) {
	tests := []struct {
		name      string
		prompt    string
		drop      bool
		wantFirst string // expected FirstPrompt after transform
	}{
		// Each noisePrefix is dropped when DropCommands is set.
		{name: "command-name dropped", prompt: "<command-name>/clear</command-name>", drop: true, wantFirst: ""},
		{name: "command-message dropped", prompt: "<command-message>running</command-message>", drop: true, wantFirst: ""},
		{name: "command-args dropped", prompt: "<command-args>--all</command-args>", drop: true, wantFirst: ""},
		{name: "local-command-stdout dropped", prompt: "<local-command-stdout>ok</local-command-stdout>", drop: true, wantFirst: ""},
		{name: "environment_context dropped", prompt: "<environment_context>cwd=/x</environment_context>", drop: true, wantFirst: ""},
		{name: "user_instructions dropped", prompt: "<user_instructions>be terse</user_instructions>", drop: true, wantFirst: ""},
		{name: "AGENTS.md preamble dropped", prompt: "# AGENTS.md instructions\nUse tabs.", drop: true, wantFirst: ""},
		{name: "leading whitespace before tag dropped", prompt: "\n  <command-name>/help</command-name>", drop: true, wantFirst: ""},

		// A normal prompt is kept even with DropCommands set.
		{name: "normal prompt kept", prompt: "add a -v flag to the CLI", drop: true, wantFirst: "add a -v flag to the CLI"},
		// A tag mentioned mid-sentence is not a prefix, so it is kept.
		{name: "mid-sentence tag mention kept", prompt: "explain what <command-name> does in the parser", drop: true, wantFirst: "explain what <command-name> does in the parser"},

		// With DropCommands unset (library zero value), the noise turn survives.
		{name: "command-name kept when DropCommands off", prompt: "<command-name>/clear</command-name>", drop: false, wantFirst: "<command-name>/clear</command-name>"},
		{name: "user_instructions kept when DropCommands off", prompt: "<user_instructions>be terse</user_instructions>", drop: false, wantFirst: "<user_instructions>be terse</user_instructions>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []cc.Entry{
				userText(tt.prompt),
				assistant("m", textBlock("acknowledged")),
			}
			s, err := Transform(entries, Options{DropCommands: tt.drop})
			if err != nil {
				t.Fatalf("Transform: %v", err)
			}
			if s == nil {
				t.Fatal("nil sample; the assistant reply should always survive")
			}
			if got := s.Meta.FirstPrompt; got != tt.wantFirst {
				t.Errorf("FirstPrompt = %q, want %q", got, tt.wantFirst)
			}
		})
	}
}

// TestTransformNoiseOnlySession verifies that a session consisting solely of a
// noise turn (a bare slash-command expansion with no assistant reply) yields a
// nil sample under DropCommands, since hasSignal then finds no real turn.
func TestTransformNoiseOnlySession(t *testing.T) {
	entries := []cc.Entry{userText("<command-name>/clear</command-name>")}
	s, err := Transform(entries, Options{DropCommands: true})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if s != nil {
		t.Fatalf("noise-only session yielded a sample, want nil: %+v", s)
	}
}

// TestFillMetaModel is a table over the model-backfill behavior. Model reflects
// the first non-empty Message.Model with no fabrication: claude carries a real
// model id, pi carries its literal config slug "default" (left verbatim, not a
// reader placeholder), and codex carries no per-message model — the codex model
// lives on a turn_context entry that Transform drops as meta — so Meta.Model is
// empty by design.
func TestFillMetaModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string // Message.Model on the assistant entry
		wantModel string
	}{
		{name: "claude model preserved", model: "claude-opus-4-8", wantModel: "claude-opus-4-8"},
		{name: "pi default slug preserved verbatim", model: "default", wantModel: "default"},
		{name: "codex empty model not fabricated", model: "", wantModel: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := []cc.Entry{
				userText("go"),
				assistant(tt.model, toolUseBlock("t1", "Bash", `{"command":"ls"}`)),
				toolResultUser(toolResultBlock("t1", "out", false)),
			}
			s, err := Transform(entries, Options{})
			if err != nil {
				t.Fatalf("Transform: %v", err)
			}
			if s == nil {
				t.Fatal("nil sample")
			}
			if got := s.Meta.Model; got != tt.wantModel {
				t.Errorf("Meta.Model = %q, want %q", got, tt.wantModel)
			}
		})
	}
}

// TestFillMetaModelFirstNonEmpty verifies fillMeta records the first non-empty
// Message.Model and does not overwrite it from a later entry. A codex-style
// session whose early turns carry no model but whose later turn does would still
// surface that model; conversely an empty later model never clears an earlier one.
func TestFillMetaModelFirstNonEmpty(t *testing.T) {
	entries := []cc.Entry{
		userText("go"),
		assistant("", toolUseBlock("t1", "Bash", `{"command":"ls"}`)), // no model on this turn
		toolResultUser(toolResultBlock("t1", "out", false)),
		assistant("claude-opus-4-8", textBlock("done")), // model appears later
	}
	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if got := s.Meta.Model; got != "claude-opus-4-8" {
		t.Errorf("Meta.Model = %q, want claude-opus-4-8 (first non-empty wins)", got)
	}
}

// TestTransformOptions covers System prepend and IncludeThinking.
func TestTransformOptions(t *testing.T) {
	base := []cc.Entry{
		{
			Type:      "user",
			CWD:       "/Users/x/app",
			GitBranch: "wip",
			Message:   &cc.Message{Role: "user", Content: jsonString("hi")},
		},
		assistant("claude-x",
			cc.ContentBlock{Type: "thinking", Text: "internal monologue"},
			textBlock("hello"),
		),
	}

	t.Run("thinking dropped by default", func(t *testing.T) {
		s, err := Transform(base, Options{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		for _, b := range s.Turns[1].Blocks {
			if b.Kind == KindThinking {
				t.Fatal("thinking block leaked when IncludeThinking is false")
			}
		}
	})

	t.Run("thinking kept when enabled", func(t *testing.T) {
		s, err := Transform(base, Options{IncludeThinking: true})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		found := false
		for _, b := range s.Turns[1].Blocks {
			if b.Kind == KindThinking && b.Text == "internal monologue" {
				found = true
			}
		}
		if !found {
			t.Fatal("thinking block missing when IncludeThinking is true")
		}
	})

	t.Run("synthetic system prepended", func(t *testing.T) {
		s, err := Transform(base, Options{System: true})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if s.Turns[0].Role != RoleSystem {
			t.Fatalf("turn[0].Role = %q, want system", s.Turns[0].Role)
		}
		got := s.Turns[0].Blocks[0].Text
		// default template includes cwd, branch, model.
		for _, want := range []string{"/Users/x/app", "wip", "claude-x"} {
			if !contains(got, want) {
				t.Errorf("system text %q missing %q", got, want)
			}
		}
	})

	t.Run("custom system text", func(t *testing.T) {
		s, err := Transform(base, Options{System: true, SystemText: "be terse"})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if got := s.Turns[0].Blocks[0].Text; got != "be terse" {
			t.Errorf("system text = %q, want %q", got, "be terse")
		}
	})
}

// TestTransformMaxToolResultBytes verifies per-payload truncation of tool_use
// inputs and tool_result content.
func TestTransformMaxToolResultBytes(t *testing.T) {
	long := `{"command":"echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	entries := []cc.Entry{
		userText("go"),
		assistant("m", toolUseBlock("t1", "Bash", long)),
		toolResultUser(toolResultBlock("t1", "0123456789ABCDEF", false)),
	}
	s, err := Transform(entries, Options{MaxToolResultBytes: 8})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	tu := s.Turns[1].Blocks[0].ToolUse
	if tu == nil {
		t.Fatal("missing tool_use")
	}
	if !contains(string(tu.Input), "truncated") {
		t.Errorf("tool_use input not truncated: %q", tu.Input)
	}
	tr := s.Turns[2].Blocks[0].ToolRes
	if tr == nil {
		t.Fatal("missing tool_result")
	}
	if !contains(tr.Content, "truncated") {
		t.Errorf("tool_result content not truncated: %q", tr.Content)
	}
	if !startsWith(tr.Content, "01234567") {
		t.Errorf("truncated content lost its head: %q", tr.Content)
	}
}

// TestTransformDanglingToolUse covers an aborted session whose final tool_use
// has no matching tool_result. It must not panic and must keep the tool_use.
func TestTransformDanglingToolUse(t *testing.T) {
	entries := []cc.Entry{
		userText("read the file"),
		assistant("m",
			textBlock("reading now"),
			toolUseBlock("t-orphan", "Read", `{"file_path":"x.go"}`),
		),
		// session ends; no tool_result follows.
	}
	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if s == nil {
		t.Fatal("nil sample for dangling tool_use session")
	}
	if got, want := len(s.Turns), 2; got != want {
		t.Fatalf("turn count = %d, want %d", got, want)
	}
	last := s.Turns[1]
	if last.Role != RoleAssistant {
		t.Fatalf("last turn role = %q, want assistant", last.Role)
	}
	foundOrphan := false
	for _, b := range last.Blocks {
		if b.Kind == KindToolUse && b.ToolUse != nil && b.ToolUse.ID == "t-orphan" {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Error("dangling tool_use was dropped; it should survive")
	}
	if s.Meta.NumToolUses != 1 {
		t.Errorf("NumToolUses = %d, want 1", s.Meta.NumToolUses)
	}
}

// TestTransformArrayToolResult covers the ~2% case where tool_result block
// Content is empty and text must be recovered from the raw block via
// cc.ExtractAnyText. We construct a block whose Content is "" but which carries
// a Text field, exercising the fallback path.
func TestTransformArrayToolResult(t *testing.T) {
	// A tool_result block with empty Content but a Text field; the fallback
	// re-encodes the block and extracts text from it.
	block := cc.ContentBlock{Type: "tool_result", ToolUseID: "t1", Text: "recovered text"}
	entries := []cc.Entry{
		userText("go"),
		assistant("m", toolUseBlock("t1", "Read", `{}`)),
		toolResultUser(block),
	}
	s, err := Transform(entries, Options{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	tr := s.Turns[2].Blocks[0].ToolRes
	if tr == nil {
		t.Fatal("missing tool_result")
	}
	if tr.Content != "recovered text" {
		t.Errorf("fallback content = %q, want %q", tr.Content, "recovered text")
	}
}

// TestWindow covers head/tail trimming with an elision marker.
func TestWindow(t *testing.T) {
	tests := []struct {
		name         string
		turns        int
		maxTurns     int
		wantMod      bool
		wantLen      int
		wantMarkerAt int // -1 if no marker expected
	}{
		{name: "no-op when within limit", turns: 4, maxTurns: 10, wantMod: false, wantLen: 4, wantMarkerAt: -1},
		{name: "no-op for non-positive max", turns: 8, maxTurns: 0, wantMod: false, wantLen: 8, wantMarkerAt: -1},
		{name: "trims to limit with marker", turns: 8, maxTurns: 5, wantMod: true, wantLen: 5, wantMarkerAt: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := buildSample(tt.turns)
			orig := len(s.Turns)
			mod := Window(s, tt.maxTurns)
			if mod != tt.wantMod {
				t.Fatalf("Window modified = %v, want %v", mod, tt.wantMod)
			}
			if got := len(s.Turns); got != tt.wantLen {
				t.Fatalf("len after window = %d, want %d", got, tt.wantLen)
			}
			if tt.wantMarkerAt >= 0 {
				marker := s.Turns[tt.wantMarkerAt]
				if marker.Role != RoleAssistant || len(marker.Blocks) != 1 {
					t.Fatalf("marker turn = %+v", marker)
				}
				if !contains(marker.Blocks[0].Text, "elided") {
					t.Errorf("marker text = %q, want an elision marker", marker.Blocks[0].Text)
				}
				// Head (first user turn) must be preserved.
				if s.Turns[0].Role != RoleUser {
					t.Errorf("head turn role = %q, want user", s.Turns[0].Role)
				}
				_ = orig
			}
		})
	}
}

// TestFingerprintFirstUser checks dedup fingerprint stability and emptiness.
func TestFingerprintFirstUser(t *testing.T) {
	a, _ := Transform([]cc.Entry{
		userText("same prompt"),
		assistant("m", textBlock("ok")),
	}, Options{})
	b, _ := Transform([]cc.Entry{
		userText("same prompt"),
		assistant("m", textBlock("different reply")),
	}, Options{})
	c, _ := Transform([]cc.Entry{
		userText("other prompt"),
		assistant("m", textBlock("ok")),
	}, Options{})

	fa, fb, fc := FingerprintFirstUser(a), FingerprintFirstUser(b), FingerprintFirstUser(c)
	if fa == "" {
		t.Fatal("fingerprint empty for a sample with a human turn")
	}
	if fa != fb {
		t.Errorf("fingerprints differ for same first prompt: %q vs %q", fa, fb)
	}
	if fa == fc {
		t.Error("fingerprints collide for different first prompts")
	}
	if got := FingerprintFirstUser(nil); got != "" {
		t.Errorf("FingerprintFirstUser(nil) = %q, want empty", got)
	}
}

// buildSample constructs a Sample with n alternating user/assistant turns.
func buildSample(n int) *Sample {
	var turns []Turn
	for i := range n {
		role := RoleUser
		if i%2 == 1 {
			role = RoleAssistant
		}
		turns = append(turns, Turn{Role: role, Blocks: []Block{{Kind: KindText, Text: "turn"}}})
	}
	return &Sample{Meta: Meta{NumTurns: n}, Turns: turns}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || indexOf(s, sub) >= 0
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
