// Package transcript renders normalized session entries as human-readable
// terminal text. It is shared by the TUI commands (ccrender, ccbrowse) so a
// session looks the same in every viewer.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tmc/cc/internal/ccmodel"
)

var (
	dimStyle  = lipgloss.NewStyle().Faint(true)
	userStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	asstStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// A Style is a set of glyphs matching one harness's native transcript look.
type Style struct {
	User       string // user prompt marker
	Asst       string // assistant text marker
	Tool       string // tool call marker
	Result     string // tool result marker (rendered indented)
	RanVerb    bool   // render shell tool calls as "Ran <cmd>" instead of Name(cmd)
	ResultRows int    // result lines shown before folding into "+N lines"
}

// Styles by harness name (as reported by cc.Harness). ClaudeStyle is the
// default for unknown harnesses.
var (
	ClaudeStyle      = Style{User: "❯", Asst: "⏺", Tool: "⏺", Result: "⎿  ", ResultRows: 1}
	CodexStyle       = Style{User: "›", Asst: "•", Tool: "•", Result: "└ ", RanVerb: true, ResultRows: 3}
	AntigravityStyle = Style{User: ">", Asst: " ", Tool: "●", Result: "⎿  ", ResultRows: 1}
	PiStyle          = Style{User: "›", Asst: " ", Tool: "⏺", Result: "⎿  ", ResultRows: 1}
)

// StyleFor returns the native style for a harness name ("claude", "codex",
// ...), defaulting to ClaudeStyle.
func StyleFor(harness string) Style {
	switch harness {
	case "codex":
		return CodexStyle
	case "antigravity", "gemini":
		return AntigravityStyle
	case "pi":
		return PiStyle
	default:
		return ClaudeStyle
	}
}

// Options controls rendering.
type Options struct {
	Timestamps bool  // prefix each line with the entry's local time
	Width      int   // wrap text to this width; 0 disables wrapping
	Style      Style // glyph set; zero value falls back to ClaudeStyle
}

// Render formats entries for display in a terminal viewport. Meta entries and
// harness preambles are dropped; tool uses render as one-line summaries and
// tool results as dimmed (or highlighted, on error) excerpts.
func Render(entries []ccmodel.Entry, opts Options) string {
	st := opts.Style
	if st.User == "" {
		st = ClaudeStyle
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsMeta || e.Message == nil {
			continue
		}
		prefix := ""
		if opts.Timestamps && !e.Timestamp.IsZero() {
			prefix = dimStyle.Render(e.Timestamp.Local().Format("15:04:05 "))
		}
		switch e.Message.Role {
		case "user":
			if e.Message.IsToolResultOnly() {
				for _, tr := range e.Message.ToolResults() {
					renderResult(&b, prefix, tr, st, opts.Width)
				}
				continue
			}
			if txt := userText(e.Message.TextContent()); txt != "" && !ccmodel.IsHarnessPreamble(txt) {
				fmt.Fprintf(&b, "%s%s %s\n\n", prefix, userStyle.Render(st.User), wrap(txt, opts.Width-2, "  "))
			}
		case "assistant":
			for _, blk := range e.Message.ContentBlocks() {
				switch blk.Type {
				case "text":
					if blk.Text != "" {
						fmt.Fprintf(&b, "%s%s %s\n\n", prefix, asstStyle.Render(st.Asst), wrap(blk.Text, opts.Width-2, "  "))
					}
				case "tool_use":
					fmt.Fprintf(&b, "%s%s\n", prefix, toolStyle.Render(clip(st.Tool+" "+toolCall(blk, st), opts.Width)))
				}
			}
		}
	}
	return b.String()
}

// renderResult writes a tool result as up to st.ResultRows lines with a
// dimmed "+N lines" fold marker, the way agent TUIs summarize output.
func renderResult(b *strings.Builder, prefix string, tr ccmodel.ContentBlock, st Style, width int) {
	txt := strings.TrimRight(resultText(tr), "\n")
	if st.RanVerb {
		txt = stripExecPreamble(txt)
	}
	if strings.TrimSpace(txt) == "" {
		return
	}
	style := dimStyle
	if tr.IsError {
		style = toolStyle
	}
	lines := strings.Split(txt, "\n")
	shown := min(len(lines), max(st.ResultRows, 1))
	for i := range shown {
		marker := st.Result
		if i > 0 {
			marker = strings.Repeat(" ", len([]rune(st.Result)))
		}
		fmt.Fprintf(b, "%s  %s\n", prefix, style.Render(clip(marker+strings.TrimRight(lines[i], " "), width-2)))
	}
	if rest := len(lines) - shown; rest > 0 {
		fmt.Fprintf(b, "%s  %s\n", prefix, dimStyle.Render(fmt.Sprintf("  … +%d lines", rest)))
	}
	b.WriteString("\n")
}

// userText strips the Antigravity prompt envelope, which wraps the actual
// request in <USER_REQUEST>…</USER_REQUEST> followed by metadata blocks the
// agent's own TUI never displays.
func userText(txt string) string {
	if _, rest, ok := strings.Cut(txt, "<USER_REQUEST>"); ok {
		if inner, _, ok := strings.Cut(rest, "</USER_REQUEST>"); ok {
			return strings.TrimSpace(inner)
		}
		return strings.TrimSpace(rest)
	}
	return txt
}

// toolCall renders a tool_use block the way its harness displays it:
// Name(detail) for Claude-style TUIs, "Ran <cmd>" for Codex shell calls.
func toolCall(blk ccmodel.ContentBlock, st Style) string {
	cmd := blk.BashCommand()
	if cmd == "" {
		cmd = codexCommand(blk.Input)
	}
	if cmd != "" && st.RanVerb {
		return "Ran " + collapse(cmd, 160)
	}
	detail := cmd
	if detail == "" {
		detail = agyDetail(blk.Input)
	}
	if detail == "" {
		detail = collapse(string(blk.Input), 120)
	}
	if detail == "" {
		return blk.Name
	}
	return blk.Name + "(" + collapse(detail, 160) + ")"
}

// agyDetail extracts the primary argument from an Antigravity tool input,
// whose field values are JSON strings encoded a second time (e.g.
// {"CommandLine":"\"go version\"","toolAction":"\"Running go version\""}).
func agyDetail(input json.RawMessage) string {
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"CommandLine", "AbsolutePath", "DirectoryPath", "toolSummary", "toolAction"} {
		v, ok := fields[key].(string)
		if !ok || v == "" {
			continue
		}
		var s string
		if err := json.Unmarshal([]byte(v), &s); err == nil {
			return s
		}
		return v
	}
	return ""
}

// codexCommand extracts the shell command from a Codex exec tool call, whose
// input wraps the command in a JavaScript tools.exec_command snippet:
//
//	{"input":"const r = await tools.exec_command({\"cmd\":\"go version\", ...})"}
func codexCommand(input json.RawMessage) string {
	var wrapper struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(input, &wrapper); err != nil || wrapper.Input == "" {
		return ""
	}
	s := wrapper.Input
	i := strings.Index(s, `"cmd":`)
	if i < 0 {
		return collapse(s, 120)
	}
	s = s[i+len(`"cmd":`):]
	// The value is a JSON string; decode it to unescape.
	var cmd string
	if err := json.Unmarshal([]byte(completeJSONString(s)), &cmd); err != nil {
		return collapse(s, 120)
	}
	return cmd
}

// completeJSONString returns the leading JSON string literal in s (which must
// start at or before the opening quote), so it can be unmarshaled alone.
func completeJSONString(s string) string {
	start := strings.IndexByte(s, '"')
	if start < 0 {
		return s
	}
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return s[start : i+1]
		}
	}
	return s[start:]
}

// stripExecPreamble drops the Codex exec-result boilerplate ("Script
// completed", "Wall time …", "Output:") so only real output remains, the way
// the Codex TUI displays it.
func stripExecPreamble(txt string) string {
	lines := strings.Split(txt, "\n")
	i := 0
	for i < len(lines) {
		l := strings.TrimSpace(lines[i])
		if l == "Output:" {
			i++
			break
		}
		if l == "" || strings.HasPrefix(l, "Script completed") ||
			strings.HasPrefix(l, "Script failed") || strings.HasPrefix(l, "Wall time") {
			i++
			continue
		}
		break
	}
	return strings.TrimLeft(strings.Join(lines[i:], "\n"), "\n")
}

// resultText extracts displayable text from a tool result, decoding
// JSON-array payloads (as Codex writes) down to their text parts.
func resultText(tr ccmodel.ContentBlock) string {
	s := strings.TrimSpace(tr.Content)
	if strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{") {
		if txt := ccmodel.ExtractAnyText(json.RawMessage(s)); txt != "" {
			return txt
		}
	}
	return s
}

// wrap word-wraps text to width, indenting continuation lines. Existing
// newlines are preserved. A width <= 0 disables wrapping.
func wrap(text string, width int, indent string) string {
	if width <= len(indent) {
		return text
	}
	// The caller's prefix occupies the first line; the join re-indents every
	// subsequent physical line, so wrapped segments are stored bare.
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		if len(line) <= width {
			out = append(out, line)
			continue
		}
		cur, lim := "", width
		for word := range strings.FieldsSeq(line) {
			switch {
			case cur == "":
				cur = word
			case len(cur)+1+len(word) <= lim:
				cur += " " + word
			default:
				out = append(out, cur)
				cur, lim = word, width-len(indent)
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return strings.Join(out, "\n"+indent)
}

// clip truncates a single line to width. A width <= 0 disables clipping.
func clip(s string, width int) string {
	if width > 0 && len(s) > width {
		return s[:width-1] + "…"
	}
	return s
}

func collapse(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		return s[:max] + "…"
	}
	return s
}
