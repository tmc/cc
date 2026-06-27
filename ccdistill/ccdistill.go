package ccdistill

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/cc"
)

// Role values for a normalized [Turn].
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Block kind values for a normalized [Block].
const (
	KindText       = "text"
	KindToolUse    = "tool_use"
	KindToolResult = "tool_result"
	KindThinking   = "thinking"
)

// Options configures [Transform]. The zero value is usable and produces a
// faithful, unwindowed trajectory; callers typically set the filters and, for
// system-prompted formats, System. Options carries only Sample-shaping knobs;
// format selection lives in the [Encoder], and discovery and splitting live in
// the caller.
type Options struct {
	// System, when true, prepends a minimal synthetic system Turn derived from
	// session metadata (model, cwd, git branch). Default false: the real
	// agent system prompt is not in the JSONL, so none is emitted.
	System bool

	// SystemText overrides the synthetic system text when System is true.
	// Empty uses the default template; see [Transform].
	SystemText string

	// DropCommands removes harness noise from user turns: slash-command
	// expansions and system/instruction/context preambles that a reader did not
	// already mark IsMeta. A turn is dropped only when its text begins with a
	// known wrapper tag (see noisePrefixes); a prompt that merely mentions such
	// a tag mid-text is kept. IsMeta and non-message entries are always dropped
	// regardless of this option.
	DropCommands bool

	// MaxToolResultBytes truncates any single tool_result content or tool_use
	// input to this many bytes (a "... [truncated N bytes]" marker is
	// appended). Zero means no truncation.
	MaxToolResultBytes int

	// IncludeThinking keeps assistant "thinking" blocks as text. Default false.
	IncludeThinking bool
}

// Block is one normalized content block within a [Turn]. Text holds rendered
// text for system, user, and assistant text. ToolUse and ToolRes are set for
// tool blocks and drive the anthropic encoder; the openai and sharegpt
// encoders render them to text.
type Block struct {
	Kind    string      `json:"kind"`                  // KindText | KindToolUse | KindToolResult | KindThinking
	Text    string      `json:"text,omitempty"`        // for text and thinking
	ToolUse *ToolUse    `json:"tool_use,omitempty"`    // for tool_use
	ToolRes *ToolResult `json:"tool_result,omitempty"` // for tool_result
}

// ToolUse is a single tool call on an assistant [Turn].
type ToolUse struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResult is a single tool result on a user [Turn].
type ToolResult struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// Turn is one normalized conversational turn after collapsing consecutive
// same-role entries.
type Turn struct {
	Role   string  `json:"role"` // RoleSystem | RoleUser | RoleAssistant
	Blocks []Block `json:"blocks"`
}

// Meta carries session-level provenance for a [Sample].
type Meta struct {
	SessionID   string `json:"session_id"`
	Source      string `json:"source"`            // file path (redacted to ~ by Redact)
	Harness     string `json:"harness,omitempty"` // producing harness: claude|codex|gemini|opencode|pi|antigravity|unknown; "" when unknown/unset
	Model       string `json:"model,omitempty"`
	CWD         string `json:"cwd,omitempty"`
	GitBranch   string `json:"git_branch,omitempty"`
	FirstPrompt string `json:"first_prompt,omitempty"`
	NumTurns    int    `json:"num_turns"`
	NumToolUses int    `json:"num_tool_uses"`
}

// Sample is one training example: a single session's normalized trajectory
// plus metadata. The zero value is not usable; obtain one from [Transform].
type Sample struct {
	Meta  Meta   `json:"meta"`
	Turns []Turn `json:"turns"`
}

// defaultSystemText is the template used when Options.System is set and
// Options.SystemText is empty.
const defaultSystemText = "You are a coding agent working in %s on branch %s using %s."

// Transform builds a Sample from one session's entries in file order. It walks
// entries sequentially (not the fork/rewind DAG) and folds them into
// normalized turns, collapsing consecutive same-role turns so that assistant
// text and tool calls that arrived as separate entries merge into one turn.
//
// Transform returns (nil, nil) — not an error — when the session yields no
// usable turns (for example, only meta or system entries). entries must be a
// single session's entries.
func Transform(entries []cc.Entry, opts Options) (*Sample, error) {
	var (
		turns []Turn
		meta  Meta
	)
	for _, e := range entries {
		if e.IsMeta || e.Message == nil {
			continue
		}
		if e.Type != RoleUser && e.Type != RoleAssistant {
			continue
		}
		fillMeta(&meta, e)

		switch e.Message.Role {
		case RoleAssistant:
			if t, ok := assistantTurn(e.Message, opts); ok {
				turns = append(turns, t)
			}
		case RoleUser:
			if e.Message.IsToolResultOnly() {
				if t, ok := toolResultTurn(e.Message); ok {
					turns = append(turns, t)
				}
				continue
			}
			if t, ok := userTurn(e.Message, opts); ok {
				turns = append(turns, t)
			}
		}
	}

	turns = collapse(turns)
	if opts.MaxToolResultBytes > 0 {
		truncateTurns(turns, opts.MaxToolResultBytes)
	}

	if !hasSignal(turns) {
		return nil, nil
	}

	if opts.System {
		turns = append([]Turn{systemTurn(meta, opts)}, turns...)
	}

	meta.FirstPrompt = firstPrompt(turns)
	meta.NumTurns = len(turns)
	meta.NumToolUses = countToolUses(turns)

	return &Sample{Meta: meta, Turns: turns}, nil
}

// fillMeta records the first non-empty session metadata seen. Model reflects
// the first non-empty Message.Model; it is empty for harnesses (such as codex)
// that carry the model at session or turn scope rather than per message, where
// the model lives on a turn_context entry that Transform drops as meta. No
// per-harness default model is fabricated.
func fillMeta(m *Meta, e cc.Entry) {
	if m.SessionID == "" {
		m.SessionID = e.SessionID
	}
	if m.CWD == "" {
		m.CWD = e.CWD
	}
	if m.GitBranch == "" {
		m.GitBranch = e.GitBranch
	}
	if m.Model == "" && e.Message != nil {
		m.Model = e.Message.Model
	}
}

// assistantTurn renders an assistant message into a Turn. It reports false when
// the message yields no blocks.
func assistantTurn(m *cc.Message, opts Options) (Turn, bool) {
	var blocks []Block
	for _, b := range m.ContentBlocks() {
		switch b.Type {
		case "text":
			if b.Text != "" {
				blocks = append(blocks, Block{Kind: KindText, Text: b.Text})
			}
		case "tool_use":
			blocks = append(blocks, Block{
				Kind:    KindToolUse,
				ToolUse: &ToolUse{ID: b.ID, Name: b.Name, Input: cloneRaw(b.Input)},
			})
		case "thinking":
			if opts.IncludeThinking && b.Text != "" {
				blocks = append(blocks, Block{Kind: KindThinking, Text: b.Text})
			}
		}
	}
	// Some assistant content arrives as a plain string rather than blocks.
	if len(m.ContentBlocks()) == 0 {
		if t := m.TextContent(); t != "" {
			blocks = append(blocks, Block{Kind: KindText, Text: t})
		}
	}
	if len(blocks) == 0 {
		return Turn{}, false
	}
	return Turn{Role: RoleAssistant, Blocks: blocks}, true
}

// toolResultTurn renders a tool-result user message into a Turn.
func toolResultTurn(m *cc.Message) (Turn, bool) {
	var blocks []Block
	for _, b := range m.ToolResults() {
		content := b.Content
		if content == "" {
			content = cc.ExtractAnyText(rawBlock(b))
		}
		blocks = append(blocks, Block{
			Kind:    KindToolResult,
			ToolRes: &ToolResult{ToolUseID: b.ToolUseID, Content: content, IsError: b.IsError},
		})
	}
	if len(blocks) == 0 {
		return Turn{}, false
	}
	return Turn{Role: RoleUser, Blocks: blocks}, true
}

// noisePrefixes are the leading wrapper tags that mark a user turn as harness
// noise rather than a human prompt: slash-command expansions and
// instruction/context preambles that a reader did not already mark IsMeta. A
// turn is dropped only when its text (after trimming leading whitespace) begins
// with one of these exact markers, so a prompt that merely mentions a tag
// mid-text is never affected. The markers are matched literally (no
// lowercasing).
var noisePrefixes = []string{
	"<command-name>",           // claude slash-command expansion
	"<command-message>",        // claude slash-command expansion
	"<command-args>",           // claude slash-command expansion
	"<local-command-stdout>",   // claude local command output echo
	"<environment_context>",    // codex/others context preamble (when not IsMeta)
	"<user_instructions>",      // codex/others instruction preamble (when not IsMeta)
	"# AGENTS.md instructions", // AGENTS.md preamble (when not IsMeta)
}

// userTurn renders a human user message into a Turn. It reports false for
// harness noise (when opts.DropCommands; see noisePrefixes) and for empty
// turns.
func userTurn(m *cc.Message, opts Options) (Turn, bool) {
	text := m.TextContent()
	if text == "" {
		return Turn{}, false
	}
	if opts.DropCommands && isNoisePrefix(text) {
		return Turn{}, false
	}
	return Turn{Role: RoleUser, Blocks: []Block{{Kind: KindText, Text: text}}}, true
}

// isNoisePrefix reports whether text begins with a known harness wrapper tag,
// after trimming leading whitespace. It matches a prefix only, so a prompt that
// merely mentions a tag mid-sentence is never dropped.
func isNoisePrefix(text string) bool {
	t := strings.TrimLeft(text, " \t\r\n")
	for _, p := range noisePrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// systemTurn builds the synthetic system turn from session metadata.
func systemTurn(m Meta, opts Options) Turn {
	text := opts.SystemText
	if text == "" {
		text = fmt.Sprintf(defaultSystemText, orNA(m.CWD), orNA(m.GitBranch), orNA(m.Model))
	}
	return Turn{Role: RoleSystem, Blocks: []Block{{Kind: KindText, Text: text}}}
}

func orNA(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// collapse merges consecutive same-role turns by concatenating their blocks.
func collapse(turns []Turn) []Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]Turn, 0, len(turns))
	for _, t := range turns {
		if n := len(out); n > 0 && out[n-1].Role == t.Role {
			out[n-1].Blocks = append(out[n-1].Blocks, t.Blocks...)
			continue
		}
		out = append(out, t)
	}
	return out
}

// truncateTurns applies a per-payload byte cap to tool inputs and results.
func truncateTurns(turns []Turn, max int) {
	for i := range turns {
		for j := range turns[i].Blocks {
			b := &turns[i].Blocks[j]
			switch b.Kind {
			case KindToolUse:
				if b.ToolUse != nil {
					b.ToolUse.Input = truncateBytes(b.ToolUse.Input, max)
				}
			case KindToolResult:
				if b.ToolRes != nil {
					b.ToolRes.Content = truncateString(b.ToolRes.Content, max)
				}
			}
		}
	}
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	dropped := len(s) - max
	return s[:max] + fmt.Sprintf("... [truncated %d bytes]", dropped)
}

// truncateBytes caps a tool_use Input to max bytes with a marker. The Input is
// emitted verbatim by the anthropic encoder as a json.RawMessage, so slicing
// the raw bytes mid-token would yield invalid JSON; the truncated payload is
// therefore re-encoded as a JSON string so the result stays a valid
// json.RawMessage.
func truncateBytes(b json.RawMessage, max int) json.RawMessage {
	if max <= 0 || len(b) <= max {
		return b
	}
	dropped := len(b) - max
	truncated := string(b[:max]) + fmt.Sprintf("... [truncated %d bytes]", dropped)
	out, err := json.Marshal(truncated)
	if err != nil {
		return b
	}
	return out
}

// hasSignal reports whether the turns include at least one assistant turn or
// one human user turn (a turn with text blocks).
func hasSignal(turns []Turn) bool {
	for _, t := range turns {
		if t.Role == RoleAssistant {
			return true
		}
		if t.Role == RoleUser && hasText(t.Blocks) {
			return true
		}
	}
	return false
}

func hasText(blocks []Block) bool {
	for _, b := range blocks {
		if b.Kind == KindText && b.Text != "" {
			return true
		}
	}
	return false
}

// firstPrompt returns the text of the first human user turn.
func firstPrompt(turns []Turn) string {
	for _, t := range turns {
		if t.Role != RoleUser {
			continue
		}
		for _, b := range t.Blocks {
			if b.Kind == KindText && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

// countToolUses counts tool_use blocks across all turns.
func countToolUses(turns []Turn) int {
	n := 0
	for _, t := range turns {
		for _, b := range t.Blocks {
			if b.Kind == KindToolUse {
				n++
			}
		}
	}
	return n
}

// cloneRaw returns a copy of raw so the Sample does not alias reader buffers.
func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(raw))
	copy(out, raw)
	return out
}

// rawBlock re-encodes a content block so cc.ExtractAnyText can recover text
// from array-shaped tool results.
func rawBlock(b cc.ContentBlock) json.RawMessage {
	raw, err := json.Marshal(b)
	if err != nil {
		return nil
	}
	return raw
}
