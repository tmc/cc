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
	dimStyle   = lipgloss.NewStyle().Faint(true)
	boldStyle  = lipgloss.NewStyle().Bold(true)
	userStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	asstStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	toolStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	plainStyle = lipgloss.NewStyle()
)

// A Style is a set of glyphs matching one harness's native transcript look.
type Style struct {
	User        string            // user prompt marker
	Asst        string            // assistant text marker
	Tool        string            // tool call marker ("" = none)
	Result      string            // tool result marker (rendered indented)
	Shell       string            // render shell tool calls as this prefix + cmd (e.g. "Ran ", "$ ")
	Empty       string            // shown for empty tool results (e.g. "(No output)"); "" hides them
	ToolCont    string            // continuation marker for wrapped tool-call lines; "" clips instead
	BareCall    bool              // render non-shell calls as "name detail" instead of Name(detail)
	Explore     bool              // group read-only shell calls under "Explored", hiding their output
	StripExec   bool              // drop Codex exec-result boilerplate from results
	HideResults bool              // omit tool result bodies (Antigravity folds them away)
	ResultRows  int               // result lines shown before folding into "+N lines"
	ResultTail  int               // result lines shown after the fold marker
	ToolNames   map[string]string // display names for recorded tool names
}

// Styles by harness name (as reported by cc.Harness). ClaudeStyle is the
// default for unknown harnesses.
var (
	ClaudeStyle = Style{User: "❯", Asst: "⏺", Tool: "⏺", Result: "⎿  ", ResultRows: 10,
		Empty: "(No output)"}
	CodexStyle = Style{User: "›", Asst: "•", Tool: "•", Result: "└ ",
		Shell: "Ran ", StripExec: true, ResultRows: 2, ResultTail: 2, ToolCont: "│ ", Explore: true}
	AntigravityStyle = Style{User: ">", Asst: " ", Tool: "●", Result: "⎿  ", ResultRows: 1,
		HideResults: true, ToolNames: map[string]string{"LS": "ListDir", "View": "Read"}}
	PiStyle = Style{User: "›", Asst: " ", Shell: "$ ", Result: "", ResultRows: 8, BareCall: true}
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
	hidden := make(map[string]bool) // tool_use IDs whose results are folded away
	inExplore := false
	// leaveExplore closes an open Explored group with the blank line the
	// Codex TUI puts after it.
	leaveExplore := func() {
		if inExplore {
			b.WriteString("\n")
			inExplore = false
		}
	}
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
				hasShown := false
				for _, tr := range e.Message.ToolResults() {
					if !hidden[tr.ToolUseID] {
						hasShown = true
					}
				}
				if hasShown {
					leaveExplore()
				}
				for _, tr := range e.Message.ToolResults() {
					if hidden[tr.ToolUseID] {
						continue
					}
					renderResult(&b, prefix, tr, e.ToolUseResult, st, opts.Width)
				}
				continue
			}
			leaveExplore()
			if txt := userText(e.Message.TextContent()); txt != "" && !ccmodel.IsHarnessPreamble(txt) {
				fmt.Fprintf(&b, "%s%s %s\n\n", prefix, userStyle.Render(st.User), wrap(txt, opts.Width-2, "  "))
			}
		case "assistant":
			for _, blk := range e.Message.ContentBlocks() {
				switch blk.Type {
				case "text":
					leaveExplore()
					if blk.Text != "" {
						fmt.Fprintf(&b, "%s%s %s\n\n", prefix, asstStyle.Render(st.Asst), wrap(mdInline(blk.Text), opts.Width-2, "  "))
					}
				case "tool_use":
					if blk.Name == "AskUserQuestion" {
						leaveExplore()
						renderQuestions(&b, prefix, blk.Input, st, opts.Width)
						continue
					}
					cmd := blk.BashCommand()
					if cmd == "" {
						cmd = codexCommand(blk.Input)
					}
					if st.Explore && cmd != "" {
						if items, ok := exploreItems(cmd); ok {
							hidden[blk.ID] = true
							if !inExplore {
								fmt.Fprintf(&b, "%s%s %s\n", prefix, dimStyle.Render(st.Tool), boldStyle.Render("Explored"))
							}
							for i, it := range items {
								marker := "    "
								if !inExplore && i == 0 {
									marker = "  └ "
								}
								fmt.Fprintf(&b, "%s%s%s\n", prefix, dimStyle.Render(marker), it)
							}
							inExplore = true
							continue
						}
					}
					leaveExplore()
					line, name := callLine(blk, cmd, st)
					if st.Tool != "" {
						line = st.Tool + " " + line
					}
					if st.ToolCont != "" {
						line = wrap(line, opts.Width, "  "+st.ToolCont)
					} else {
						line = clip(line, opts.Width)
					}
					if name != "" {
						line = strings.Replace(line, name, boldStyle.Render(name), 1)
					}
					fmt.Fprintf(&b, "%s%s\n", prefix, line)
				}
			}
		}
	}
	return b.String()
}

// renderResult writes a tool result as up to st.ResultRows lines with a
// dimmed "+N lines" fold marker, the way agent TUIs summarize output.
func renderResult(b *strings.Builder, prefix string, tr ccmodel.ContentBlock, tur *ccmodel.ToolUseResult, st Style, width int) {
	if st.HideResults {
		return
	}
	txt := resultSummary(tur)
	if txt == "" {
		txt = strings.TrimRight(resultText(tr), "\n")
		if st.StripExec {
			txt = stripExecPreamble(txt)
		}
	}
	if strings.TrimSpace(txt) == "" {
		if st.Empty == "" {
			return
		}
		txt = st.Empty
	}
	// Native TUIs render result content in the default color; only the
	// marker and fold hint are dimmed. Errors highlight the content too.
	style := plainStyle
	if tr.IsError {
		style = toolStyle
	}
	cont := strings.Repeat(" ", len([]rune(st.Result)))
	// Fold on content-bearing lines only; blank rows waste the excerpt.
	var all []string
	for l := range strings.SplitSeq(txt, "\n") {
		if strings.TrimSpace(l) != "" {
			all = append(all, l)
		}
	}
	emit := func(i int, first bool) {
		marker := cont
		if first {
			marker = st.Result
		}
		fmt.Fprintf(b, "%s  %s%s\n", prefix, dimStyle.Render(marker),
			style.Render(clip(strings.TrimRight(all[i], " "), width-2-len([]rune(marker)))))
	}
	head := min(len(all), max(st.ResultRows, 1))
	tail := 0
	if rest := len(all) - head; rest > 0 {
		tail = min(st.ResultTail, rest)
	}
	for i := range head {
		emit(i, i == 0)
	}
	if rest := len(all) - head - tail; rest > 0 {
		fmt.Fprintf(b, "%s  %s\n", prefix, dimStyle.Render(fmt.Sprintf("%s… +%d lines", cont, rest)))
	}
	for i := len(all) - tail; i < len(all); i++ {
		emit(i, false)
	}
	b.WriteString("\n")
}

// resultSummary condenses a structured toolUseResult into the one-line
// summary Claude Code shows ("Read 41 lines", "Found 3 files") instead of
// the raw content excerpt. It returns "" when no structure applies.
func resultSummary(tur *ccmodel.ToolUseResult) string {
	if tur == nil {
		return ""
	}
	switch {
	case tur.File != nil && tur.File.NumLines > 0:
		return fmt.Sprintf("Read %d lines", tur.File.NumLines)
	case tur.FilePath != "" && (tur.OldString != "" || tur.NewString != ""):
		return "Updated " + tur.FilePath
	case tur.NumFiles > 0:
		return fmt.Sprintf("Found %d files", tur.NumFiles)
	case tur.NumMatches > 0:
		return fmt.Sprintf("Found %d matches", tur.NumMatches)
	}
	return ""
}

// renderQuestions expands an AskUserQuestion tool call into its questions
// and options, the way the interactive dialog presents them, instead of the
// one-line raw-input summary used for other tools.
func renderQuestions(b *strings.Builder, prefix string, input json.RawMessage, st Style, width int) {
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label string `json:"label"`
			} `json:"options"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(input, &in); err != nil || len(in.Questions) == 0 {
		fmt.Fprintf(b, "%s%s\n", prefix, toolStyle.Render(clip(st.Tool+" AskUserQuestion", width)))
		return
	}
	fmt.Fprintf(b, "%s%s\n", prefix, toolStyle.Render(fmt.Sprintf("%s AskUserQuestion (%d)", st.Tool, len(in.Questions))))
	for _, q := range in.Questions {
		head := q.Header
		if q.MultiSelect {
			head += " (multi)"
		}
		fmt.Fprintf(b, "%s  %s %s\n", prefix, toolStyle.Render("["+head+"]"), wrap(q.Question, width-4, "  "))
		for _, o := range q.Options {
			fmt.Fprintf(b, "%s    %s\n", prefix, dimStyle.Render(clip("○ "+o.Label, width-4)))
		}
	}
	b.WriteString("\n")
}

// mdInline strips inline markdown emphasis markers (**bold**, `code`) that
// agent TUIs render as styling rather than literal characters. Only paired
// markers are removed, so stray asterisks and backticks survive.
func mdInline(txt string) string {
	for _, m := range []string{"**", "`"} {
		if strings.Count(txt, m)%2 == 0 {
			txt = strings.ReplaceAll(txt, m, "")
		}
	}
	// Heading markers likewise render as styling, not literal hashes.
	lines := strings.Split(txt, "\n")
	for i, l := range lines {
		if t := strings.TrimLeft(l, "#"); t != l && strings.HasPrefix(t, " ") {
			lines[i] = strings.TrimSpace(t)
		}
	}
	return strings.Join(lines, "\n")
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

// callLine renders a tool_use block the way its harness displays it:
// Name(detail) for Claude-style TUIs, a Shell prefix ("Ran <cmd>", "$ <cmd>")
// for shell calls in harnesses that render them bare, and "name detail" for
// pi's bare-call style. It also returns the display name so the caller can
// embolden it after width handling.
func callLine(blk ccmodel.ContentBlock, cmd string, st Style) (line, name string) {
	if cmd != "" && st.Shell != "" {
		return st.Shell + collapse(cmd, 160), ""
	}
	detail := cmd
	if detail == "" {
		detail = primaryArg(blk.Input)
		if detail != "" {
			detail += lineRange(blk.Input, st)
		}
	}
	if detail == "" {
		detail = agyDetail(blk.Input)
	}
	if detail == "" {
		detail = collapse(string(blk.Input), 120)
	}
	name = blk.Name
	if n, ok := st.ToolNames[name]; ok {
		name = n
	}
	if detail == "" {
		return name, name
	}
	if st.BareCall {
		return name + " " + collapse(detail, 160), name
	}
	return name + "(" + collapse(detail, 160) + ")", name
}

// lineRange renders a Read-style offset/limit input as the line-range suffix
// the harness shows: " · lines 1-15" for Claude, ":1-15" for pi.
func lineRange(input json.RawMessage, st Style) string {
	var f struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	if err := json.Unmarshal(input, &f); err != nil || f.Limit <= 0 {
		return ""
	}
	start := max(f.Offset, 1)
	end := start + f.Limit - 1
	if st.BareCall {
		return fmt.Sprintf(":%d-%d", start, end)
	}
	return fmt.Sprintf(" · lines %d-%d", start, end)
}

// exploreItems classifies a shell command as read-only exploration the way
// the Codex TUI does, splitting on && and mapping each part to a display
// verb ("List cmd", "Read go.mod"). ok is false if any part is not clearly
// read-only, in which case the command renders as a normal shell call.
func exploreItems(cmd string) (items []string, ok bool) {
	for part := range strings.SplitSeq(cmd, "&&") {
		// A trailing pipeline (| head) doesn't change read-only-ness.
		part, _, _ = strings.Cut(part, "|")
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		arg := fields[len(fields)-1]
		switch fields[0] {
		case "ls":
			if len(fields) == 1 || strings.HasPrefix(arg, "-") {
				arg = "."
			}
			items = append(items, "List "+arg)
		case "cat", "head", "tail":
			items = append(items, "Read "+arg)
		case "sed":
			// Only the print form (sed -n '1,15p' file) is a read.
			if len(fields) < 2 || fields[1] != "-n" {
				return nil, false
			}
			items = append(items, "Read "+arg)
		case "rg", "grep":
			items = append(items, "Search "+arg)
		case "pwd", "wc", "file", "stat":
			items = append(items, "Read "+arg)
		default:
			return nil, false
		}
	}
	return items, len(items) > 0
}

// primaryArg extracts the argument agent TUIs headline for a tool call —
// the file being edited, the pattern being searched for — instead of the
// raw input JSON.
func primaryArg(input json.RawMessage) string {
	var fields map[string]any
	if err := json.Unmarshal(input, &fields); err != nil {
		return ""
	}
	for _, key := range []string{"file_path", "path", "pattern", "query", "url", "skill", "description", "prompt", "subject"} {
		if v, ok := fields[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
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
