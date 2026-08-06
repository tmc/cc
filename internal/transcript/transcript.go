// Package transcript renders normalized session entries as human-readable
// terminal text. It is shared by the TUI commands (ccrender, ccbrowse) so a
// session looks the same in every viewer.
package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
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
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	plainStyle = lipgloss.NewStyle()
)

// A Style is a set of glyphs matching one harness's native transcript look.
type Style struct {
	User        string            // user prompt marker
	Asst        string            // assistant text marker
	Tool        string            // tool call marker ("" = none)
	Result      string            // tool result marker (rendered indented)
	Thinking    string            // thinking block marker; "" hides thinking
	Shell       string            // render shell tool calls as this prefix + cmd (e.g. "Ran ", "$ ")
	Empty       string            // shown for empty tool results (e.g. "(No output)"); "" hides them
	ToolCont    string            // continuation marker for wrapped tool-call lines; "" clips instead
	BareCall    bool              // render non-shell calls as "name detail" instead of Name(detail)
	HeadHash    bool              // keep heading hashes: "### Title" bold (pi), vs bold text only
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
	ClaudeStyle = Style{User: "❯", Asst: "⏺", Tool: "⏺", Result: "⎿  ", Thinking: "∴", ResultRows: 10,
		Empty: "(No output)"}
	CodexStyle = Style{User: "›", Asst: "•", Tool: "•", Result: "└ ", Thinking: "•",
		Shell: "Ran ", StripExec: true, ResultRows: 2, ResultTail: 2, ToolCont: "│ ", Explore: true}
	AntigravityStyle = Style{User: ">", Asst: " ", Tool: "●", Result: "⎿  ", Thinking: "∴", ResultRows: 1,
		HideResults: true, ToolNames: map[string]string{"LS": "ListDir", "View": "Read"}}
	PiStyle = Style{User: "›", Asst: " ", Shell: "$ ", Result: "", Thinking: " ", ResultRows: 8, BareCall: true,
		HeadHash: true}
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
		if e.Type == "system" {
			switch e.Subtype {
			case "compact_boundary":
				leaveExplore()
				msg := e.Content
				if msg == "" {
					msg = "Conversation compacted"
				}
				fmt.Fprintf(&b, "%s\n\n", dimStyle.Render("✻ "+msg))
			case "turn_duration":
				if e.DurationMs >= 1000 {
					leaveExplore()
					fmt.Fprintf(&b, "%s\n\n", dimStyle.Render(fmt.Sprintf("✻ Worked for %ds", e.DurationMs/1000)))
				}
			}
			continue
		}
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
			txt := userText(e.Message.TextContent())
			if txt == "" || ccmodel.IsHarnessPreamble(txt) {
				continue
			}
			if e.IsCompactSummary {
				fmt.Fprintf(&b, "%s%s\n\n", prefix, dimStyle.Render("✻ Conversation compacted"))
				continue
			}
			// Slash commands persist as XML-tagged user entries; the native
			// TUI shows the bare command and its captured stdout.
			if name := tagText(txt, "command-name"); name != "" {
				if args := tagText(txt, "command-args"); args != "" {
					name += " " + args
				}
				fmt.Fprintf(&b, "%s%s %s\n\n", prefix, userStyle.Render(st.User), name)
				continue
			}
			if strings.Contains(txt, "<local-command-stdout>") {
				if out := tagText(txt, "local-command-stdout"); out != "" {
					renderResult(&b, prefix, ccmodel.ContentBlock{Content: out}, nil, st, opts.Width)
					b.WriteString("\n")
				}
				continue
			}
			fmt.Fprintf(&b, "%s%s %s\n\n", prefix, userStyle.Render(st.User), wrap(txt, opts.Width-2, "  "))
		case "assistant":
			for _, blk := range e.Message.ContentBlocks() {
				switch blk.Type {
				case "text":
					leaveExplore()
					if blk.Text != "" {
						if e.IsAPIErrorMessage {
							fmt.Fprintf(&b, "%s%s %s\n\n", prefix, errStyle.Render(st.Asst), errStyle.Render(wrap(blk.Text, opts.Width-2, "  ")))
							continue
						}
						txt := boldHeads(boldSpans(colorCode(wrap(mdInline(blk.Text), opts.Width-2, "  "))), st.HeadHash)
						fmt.Fprintf(&b, "%s%s %s\n\n", prefix, asstStyle.Render(st.Asst), txt)
					}
				case "thinking":
					if st.Thinking == "" {
						continue
					}
					txt := blk.Thinking
					if txt == "" {
						txt = blk.Text
					}
					if txt != "" {
						leaveExplore()
						// Thinking stays uniformly dim: strip bold markers
						// rather than styling them, since an inner ANSI reset
						// would cancel the dim for the rest of the line.
						txt = strings.ReplaceAll(txt, "**", "")
						fmt.Fprintf(&b, "%s%s %s\n\n", prefix, dimStyle.Render(st.Thinking), dimStyle.Render(wrap(txt, opts.Width-2, "  ")))
					}
				case "tool_use":
					if blk.Name == "AskUserQuestion" || blk.Name == "ask_question" {
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
	case len(tur.Answers) > 0:
		var lines []string
		seen := make(map[string]bool)
		for _, q := range tur.Questions {
			if a, ok := tur.Answers[q.Question]; ok {
				lines = append(lines, "· "+q.Question+" → "+a)
				seen[q.Question] = true
			}
		}
		var rest []string
		for q, a := range tur.Answers {
			if !seen[q] {
				rest = append(rest, "· "+q+" → "+a)
			}
		}
		slices.Sort(rest)
		return strings.Join(append(lines, rest...), "\n")
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
	qs := parseQuestions(input)
	if len(qs) == 0 {
		fmt.Fprintf(b, "%s%s\n", prefix, toolStyle.Render(clip(st.Tool+" AskUserQuestion", width)))
		return
	}
	fmt.Fprintf(b, "%s%s\n", prefix, toolStyle.Render(fmt.Sprintf("%s AskUserQuestion (%d)", st.Tool, len(qs))))
	for _, q := range qs {
		head := q.header
		if q.multi {
			head += " (multi)"
		}
		if head != "" {
			head = toolStyle.Render("["+head+"]") + " "
		}
		fmt.Fprintf(b, "%s  %s%s\n", prefix, head, wrap(q.question, width-4, "  "))
		for _, o := range q.options {
			fmt.Fprintf(b, "%s    %s\n", prefix, dimStyle.Render(clip("○ "+o, width-4)))
		}
	}
	b.WriteString("\n")
}

type askedQuestion struct {
	question, header string
	multi            bool
	options          []string
}

// parseQuestions decodes a question-dialog tool input in either Claude's
// AskUserQuestion shape ({"questions":[{question,header,multiSelect,
// options:[{label}]}]}) or Antigravity's ask_question shape, whose
// "questions" value is a JSON array encoded a second time as a string, with
// is_multi_select and bare option strings.
func parseQuestions(input json.RawMessage) []askedQuestion {
	var raw struct {
		Questions json.RawMessage `json:"questions"`
	}
	if json.Unmarshal(input, &raw) != nil || len(raw.Questions) == 0 {
		return nil
	}
	arr := raw.Questions
	var double string
	if json.Unmarshal(arr, &double) == nil {
		arr = json.RawMessage(double)
	}
	var qs []struct {
		Question      string          `json:"question"`
		Header        string          `json:"header"`
		MultiSelect   bool            `json:"multiSelect"`
		IsMultiSelect bool            `json:"is_multi_select"`
		Options       json.RawMessage `json:"options"`
	}
	if json.Unmarshal(arr, &qs) != nil {
		return nil
	}
	var out []askedQuestion
	for _, q := range qs {
		aq := askedQuestion{question: q.Question, header: q.Header, multi: q.MultiSelect || q.IsMultiSelect}
		var labeled []struct {
			Label string `json:"label"`
		}
		if json.Unmarshal(q.Options, &labeled) == nil && len(labeled) > 0 && labeled[0].Label != "" {
			for _, o := range labeled {
				aq.options = append(aq.options, o.Label)
			}
		} else {
			var plain []string
			if json.Unmarshal(q.Options, &plain) == nil {
				aq.options = plain
			}
		}
		out = append(out, aq)
	}
	return out
}

// codeRe matches an inline code span on a single line.
var codeRe = regexp.MustCompile("`([^`\n]+)`")

// codeStyle colors inline code spans the way agent TUIs do.
var codeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

// colorCode replaces `code` spans with colorized text (backticks dropped).
// It runs after wrapping so styling codes don't skew width accounting; a
// span split across wrapped lines keeps its backticks.
func colorCode(txt string) string {
	return codeRe.ReplaceAllStringFunc(txt, func(m string) string {
		return codeStyle.Render(m[1 : len(m)-1])
	})
}

// linkRe matches an inline markdown link, [label](target).
var linkRe = regexp.MustCompile(`\[([^\[\]\n]+)\]\([^()\n]+\)`)

// mdInline strips inline markdown that agent TUIs render as styling rather
// than literal characters: [label](target) links collapse to their label.
// **bold** markers and heading lines pass through untouched — boldSpans and
// boldHeads style them after wrapping.
func mdInline(txt string) string {
	txt = linkRe.ReplaceAllString(txt, "$1")
	// Code-fence lines drop away with their content left in place.
	var lines []string
	for l := range strings.SplitSeq(txt, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			continue
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

var boldRe = regexp.MustCompile(`\*\*([^*\n]+)\*\*`)

// boldSpans styles **bold** spans after wrapping, then strips any markers
// left dangling by a span the wrap split across lines.
func boldSpans(txt string) string {
	txt = boldRe.ReplaceAllStringFunc(txt, func(m string) string {
		return boldStyle.Render(m[2 : len(m)-2])
	})
	return strings.ReplaceAll(txt, "**", "")
}

var headRe = regexp.MustCompile(`(?m)^([ \t]*)(#{1,6}) (.*)$`)

// boldHeads styles markdown heading lines after wrapping, when the ANSI codes
// no longer affect width accounting. Most TUIs bold the heading text and drop
// the hashes; pi bolds the whole "### Title" line, hashes included.
func boldHeads(txt string, keepHash bool) string {
	return headRe.ReplaceAllStringFunc(txt, func(m string) string {
		sub := headRe.FindStringSubmatch(m)
		if keepHash {
			return sub[1] + boldStyle.Render(sub[2]+" "+sub[3])
		}
		return sub[1] + boldStyle.Render(sub[3])
	})
}

// tagText returns the trimmed content of the first <tag>…</tag> span in txt,
// or "" if the tag is absent.
func tagText(txt, tag string) string {
	_, rest, ok := strings.Cut(txt, "<"+tag+">")
	if !ok {
		return ""
	}
	inner, _, _ := strings.Cut(rest, "</"+tag+">")
	return strings.TrimSpace(inner)
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
