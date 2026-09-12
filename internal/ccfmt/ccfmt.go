// Package ccfmt renders a normalized session as text, markdown, JSON, or a
// terminal transcript. It is the body of the ccfmt command, shared so that
// other commands (ccrender -format) format a session by calling it rather
// than by running ccfmt as a subprocess.
package ccfmt

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/transcript"
)

// Options select the output format and how much of each entry it carries.
// Use [Default] to get the values the ccfmt command starts from; the zero
// value formats nothing.
type Options struct {
	Format       string // transcript, text, markdown, json, or jsonl
	Width        int    // wrap width for the transcript format; 0 uses $COLUMNS, else 100
	Title        string // optional document title
	Meta         bool   // include session metadata and injected preamble messages
	Timestamps   bool   // include per-entry timestamps
	MaxBytes     int    // cap on a single text or tool-result block; 0 means unlimited
	Cleanup      string // preset applied over the rest: none, publish, or digest
	Commentary   bool   // include commentary-phase assistant messages
	Tools        string // tool call rendering: full, summary, or omit
	ToolResults  string // tool result rendering: full, summary, or omit
	InlineImages bool   // inline message images as data URLs in markdown
	RedactImages bool   // pass inline image bytes through the redactor
	Redact       bool   // redact obvious secrets such as Stripe test keys
	Since        string // how far back to search when resolving a session ID

	// Explicit records the option names the caller set explicitly, in their
	// flag spelling ("tools", "tool-results", "max-bytes"). A cleanup preset
	// only fills in what the caller left alone.
	Explicit map[string]bool
}

// Default returns the options the ccfmt command uses when given no flags.
func Default() Options {
	return Options{
		Format:       "transcript",
		MaxBytes:     4000,
		Cleanup:      "none",
		Commentary:   true,
		Tools:        "full",
		ToolResults:  "full",
		InlineImages: true,
		Redact:       true,
		Since:        "30d",
	}
}

// A Formatter writes sessions in one configuration. It is not safe for
// concurrent use.
type Formatter struct {
	opts Options
}

// New returns a Formatter for opts, with any cleanup preset applied.
func New(opts Options) (*Formatter, error) {
	f := &Formatter{opts: opts}
	f.applyCleanupPreset()
	if err := f.validateModes(); err != nil {
		return nil, err
	}
	return f, nil
}

var defaultRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bpk_test_[A-Za-z0-9_]+\b`), "pk_test_XXXXX..."},
	{regexp.MustCompile(`\bsk_test_[A-Za-z0-9_]+\b`), "sk_test_XXXXX..."},
}

var redactImageDataFunc = redactImageData

// Format reads the sessions named in args (or standard input when args is
// empty) and writes them in the configured format.
func (f *Formatter) Format(w io.Writer, args []string) error {
	entries, source, err := f.readEntries(args)
	if err != nil {
		return err
	}

	switch f.opts.Format {
	case "text":
		return f.writeText(w, source, entries)
	case "transcript":
		return f.writeTranscript(w, source, entries)
	case "markdown", "md":
		return f.writeMarkdown(w, source, entries)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "jsonl":
		enc := json.NewEncoder(w)
		for _, e := range entries {
			if !f.shouldIncludeEntry(e) {
				continue
			}
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q", f.opts.Format)
	}
}

func (f *Formatter) readEntries(args []string) ([]cc.Entry, string, error) {
	if len(args) == 0 {
		entries, err := cc.ReadAll(context.Background(), os.Stdin)
		return entries, "stdin", err
	}

	since, err := ccpaths.ParseDuration(f.opts.Since)
	if err != nil {
		return nil, "", fmt.Errorf("parse -since: %w", err)
	}
	files := make([]string, len(args))
	for i, arg := range args {
		files[i], err = cc.ResolveSessionFile(context.Background(), arg, since)
		if err != nil {
			return nil, "", err
		}
	}

	var all []cc.Entry
	for _, path := range files {
		entries, err := cc.ReadFileWithSubagents(context.Background(), path)
		if err != nil {
			return nil, "", err
		}
		all = append(all, entries...)
	}
	return all, strings.Join(files, ", "), nil
}

func (f *Formatter) writeText(w io.Writer, source string, entries []cc.Entry) error {
	meta := summarize(entries)
	if meta.title != "" {
		fmt.Fprintln(w, meta.title)
		fmt.Fprintln(w, strings.Repeat("=", len(meta.title)))
	}
	if source != "" {
		fmt.Fprintf(w, "Source: %s\n", source)
	}
	if meta.sessionID != "" {
		fmt.Fprintf(w, "Session: %s\n", meta.sessionID)
	}
	if meta.cwd != "" {
		fmt.Fprintf(w, "Workspace: %s\n", meta.cwd)
	}
	if !meta.started.IsZero() {
		fmt.Fprintf(w, "Started: %s\n", meta.started.Local().Format(time.RFC3339))
	}
	fmt.Fprintln(w)

	for _, e := range entries {
		if !f.shouldIncludeEntry(e) {
			continue
		}
		if err := f.writeTextEntry(w, e); err != nil {
			return err
		}
	}
	return nil
}

// writeTranscript renders entries the way the ccrender TUI does, using the
// native glyphs of the harness that recorded source. Unlike the other formats
// it is a view, not an export: the document flags (-max-bytes, -tools,
// -cleanup, ...) do not apply.
func (f *Formatter) writeTranscript(w io.Writer, source string, entries []cc.Entry) error {
	_, err := io.WriteString(w, transcript.Render(entries, transcript.Options{
		Timestamps: f.opts.Timestamps,
		Width:      f.transcriptWidth(),
		Style:      transcript.StyleFor(cc.Harness(source)),
	}))
	return err
}

// transcriptWidth resolves the wrap width: the -width flag, else $COLUMNS,
// else a terminal-typical 100.
func (f *Formatter) transcriptWidth() int {
	if f.opts.Width > 0 {
		return f.opts.Width
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 100
}

func (f *Formatter) writeTextEntry(w io.Writer, e cc.Entry) error {
	var body strings.Builder
	switch {
	case e.Type == "system" && e.Subtype == "compact_boundary":
		fmt.Fprintln(&body, "Conversation compacted.")
	case e.IsCompactSummary:
		fmt.Fprintln(&body, f.compactText(e))
	case isToolResultEntry(e):
		if f.opts.ToolResults == "omit" {
			return nil
		}
		for _, block := range e.Message.ToolResults() {
			text := f.toolResultText(block)
			if strings.TrimSpace(text) == "" {
				continue
			}
			fmt.Fprintln(&body, text)
		}
	default:
		if text := f.entryBody(e); text != "" {
			fmt.Fprintln(&body, f.limitText(text))
		}
		if e.Message != nil && e.Type == "assistant" && f.opts.Tools != "omit" {
			for _, tool := range e.Message.ToolUses() {
				fmt.Fprintln(&body, f.textToolLine(tool))
			}
		}
	}
	// Harness bookkeeping records (mode changes, bridge sessions, prompt
	// snapshots) render no body; a bare label helps no one, so drop them
	// unless -meta asks for the raw record stream.
	if body.Len() == 0 && !f.opts.Meta {
		return nil
	}

	label := textLabel(e)
	if f.opts.Timestamps && !e.Timestamp.IsZero() {
		label += " " + e.Timestamp.Local().Format("15:04:05")
	}
	fmt.Fprintf(w, "[%s]\n", label)
	if _, err := io.WriteString(w, body.String()); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (f *Formatter) writeMarkdown(w io.Writer, source string, entries []cc.Entry) error {
	meta := summarize(entries)
	title := f.opts.Title
	if title == "" {
		title = meta.title
	}
	if title == "" {
		title = "Coding Session"
	}

	fmt.Fprintf(w, "# %s\n\n", title)
	if meta.sessionID != "" {
		fmt.Fprintf(w, "- Session: `%s`\n", meta.sessionID)
	}
	if source != "" {
		fmt.Fprintf(w, "- Source: `%s`\n", source)
	}
	if meta.cwd != "" {
		fmt.Fprintf(w, "- Workspace: `%s`\n", meta.cwd)
	}
	if !meta.started.IsZero() {
		fmt.Fprintf(w, "- Started: `%s`\n", meta.started.Local().Format(time.RFC3339))
	}
	fmt.Fprintln(w)

	for _, e := range entries {
		if !f.shouldIncludeEntry(e) {
			continue
		}
		if err := f.writeMarkdownEntry(w, e); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) writeMarkdownEntry(w io.Writer, e cc.Entry) error {
	var body bytes.Buffer
	if err := f.writeMarkdownEntryBody(&body, e); err != nil {
		return err
	}
	// Same as writeTextEntry: a body-less entry would be a bare heading.
	if body.Len() == 0 && !f.opts.Meta {
		return nil
	}
	fmt.Fprintf(w, "## %s\n\n", markdownLabel(e))
	if f.opts.Timestamps && !e.Timestamp.IsZero() {
		fmt.Fprintf(w, "_%s_\n\n", e.Timestamp.Local().Format(time.RFC3339))
	}
	_, err := w.Write(body.Bytes())
	return err
}

func (f *Formatter) writeMarkdownEntryBody(w io.Writer, e cc.Entry) error {
	switch {
	case e.Type == "system" && e.Subtype == "compact_boundary":
		if _, err := fmt.Fprintln(w, "_Conversation compacted._"); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w)
		return err
	case e.IsCompactSummary:
		return writeFence(w, "", f.compactText(e))
	case isToolResultEntry(e):
		if f.opts.ToolResults == "omit" {
			return nil
		}
		for _, block := range e.Message.ToolResults() {
			if err := f.writeMarkdownToolResult(w, block); err != nil {
				return err
			}
		}
		return nil
	default:
		text := f.entryBody(e)
		if text != "" {
			if err := writeMarkdownTranscriptBlock(w, f.limitText(text)); err != nil {
				return err
			}
		}
		if err := f.writeMarkdownImages(w, e); err != nil {
			return err
		}
		if e.Message != nil && e.Type == "assistant" && f.opts.Tools != "omit" {
			for _, tool := range e.Message.ToolUses() {
				if f.opts.Tools == "summary" {
					fmt.Fprintf(w, "- %s\n", f.markdownToolLine(tool))
					continue
				}
				fmt.Fprintf(w, "### Tool: `%s`\n\n", tool.Name)
				if err := writeFence(w, toolLanguage(tool), f.toolBlock(tool)); err != nil {
					return err
				}
				fmt.Fprintln(w)
			}
			if f.opts.Tools == "summary" && len(e.Message.ToolUses()) > 0 {
				fmt.Fprintln(w)
			}
		}
		return nil
	}
}

type sessionMeta struct {
	title        string
	sessionID    string
	cwd          string
	distinctCWDs []string
	started      time.Time
}

func summarize(entries []cc.Entry) sessionMeta {
	var meta sessionMeta
	seen := map[string]bool{}
	for _, e := range entries {
		if meta.sessionID == "" && e.SessionID != "" {
			meta.sessionID = e.SessionID
		}
		if e.CWD != "" {
			meta.cwd = e.CWD
			if !seen[e.CWD] {
				seen[e.CWD] = true
				meta.distinctCWDs = append(meta.distinctCWDs, e.CWD)
			}
		}
		if meta.started.IsZero() && !e.Timestamp.IsZero() {
			meta.started = e.Timestamp
		}
		if meta.title == "" && e.CustomTitle != "" {
			meta.title = e.CustomTitle
		}
	}
	if meta.title == "" && meta.cwd != "" {
		meta.title = filepath.Base(meta.cwd)
	}
	return meta
}

func (f *Formatter) shouldIncludeEntry(e cc.Entry) bool {
	if isNoiseEntry(e) {
		return false
	}
	if f.opts.Meta {
		if !f.opts.Commentary && isCommentary(e) {
			return false
		}
		return !f.omitEntryByMode(e)
	}
	if e.IsMeta {
		return false
	}
	if e.Type == "system" && e.Subtype == "session_meta" {
		return false
	}
	if !f.opts.Commentary && isCommentary(e) {
		return false
	}
	return !f.omitEntryByMode(e)
}

func textLabel(e cc.Entry) string {
	switch {
	case e.IsCompactSummary:
		return decorateTextLabel("compact summary", e)
	case e.Type == "system" && e.Subtype == "compact_boundary":
		return decorateTextLabel("compacted", e)
	case isToolResultEntry(e):
		return decorateTextLabel("tool result", e)
	case e.Message != nil && e.Message.Role != "":
		return decorateTextLabel(e.Message.Role, e)
	default:
		return decorateTextLabel(e.Type, e)
	}
}

func markdownLabel(e cc.Entry) string {
	switch {
	case e.IsCompactSummary:
		return decorateMarkdownLabel("Compact Summary", e)
	case e.Type == "system" && e.Subtype == "compact_boundary":
		return decorateMarkdownLabel("Compacted", e)
	case isToolResultEntry(e):
		return decorateMarkdownLabel("Tool Result", e)
	case e.Message != nil && e.Message.Role != "":
		return decorateMarkdownLabel(titleCase(e.Message.Role), e)
	default:
		return decorateMarkdownLabel(titleCase(e.Type), e)
	}
}

// titleCase upper-cases the first letter of each space-separated word. The role
// and type values it formats are simple ASCII identifiers, so this avoids the
// deprecated strings.Title and a golang.org/x/text dependency.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func decorateTextLabel(base string, e cc.Entry) string {
	if ctx := entryContext(e); ctx != "" {
		return base + " [" + ctx + "]"
	}
	return base
}

func decorateMarkdownLabel(base string, e cc.Entry) string {
	if ctx := entryContext(e); ctx != "" {
		return base + " (" + ctx + ")"
	}
	return base
}

func entryContext(e cc.Entry) string {
	var parts []string
	if e.Phase != "" {
		parts = append(parts, e.Phase)
	}
	if actor := entryActor(e); actor != "" {
		parts = append(parts, actor)
	}
	return strings.Join(parts, ", ")
}

func entryActor(e cc.Entry) string {
	switch {
	case e.AgentName != "" && e.TeamName != "":
		return "agent " + e.AgentName + "@" + e.TeamName
	case e.AgentName != "":
		return "agent " + e.AgentName
	case e.AgentID != "":
		return "subagent " + e.AgentID
	case e.IsSidechain:
		return "subagent"
	default:
		return ""
	}
}

func (f *Formatter) entryBody(e cc.Entry) string {
	if e.Attachment != nil {
		return attachmentText(e.Attachment)
	}
	if e.Message != nil {
		if blocks := e.Message.ContentBlocks(); blocks != nil {
			var parts []string
			for _, block := range blocks {
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					parts = append(parts, f.redactText(strings.TrimSpace(block.Text)))
				}
			}
			return strings.Join(parts, "\n\n")
		}
		return f.redactText(strings.TrimSpace(e.Message.TextContent()))
	}
	if e.Summary != "" {
		return f.redactText(strings.TrimSpace(e.Summary))
	}
	if e.Content != "" {
		return f.redactText(strings.TrimSpace(e.Content))
	}
	return ""
}

func attachmentText(a *cc.Attachment) string {
	switch a.Type {
	case "workflow_keyword_request":
		return "Workflow mode requested."
	case "deferred_tools_delta":
		var raw struct {
			AddedNames []string `json:"addedNames"`
		}
		if json.Unmarshal(a.Raw, &raw) == nil && len(raw.AddedNames) > 0 {
			return "Deferred tools added: " + strings.Join(raw.AddedNames, ", ")
		}
	case "task_reminder":
		return "Task reminder."
	}
	return ""
}

func entryImages(e cc.Entry) []cc.ContentBlock {
	if e.Message == nil {
		return nil
	}
	return e.Message.ImageBlocks()
}

func (f *Formatter) compactText(e cc.Entry) string {
	text := f.entryBody(e)
	if text != "" {
		return text
	}
	return "Conversation compacted."
}

func isToolResultEntry(e cc.Entry) bool {
	return e.Message != nil && len(e.Message.ToolResults()) > 0
}

func isCommentary(e cc.Entry) bool {
	return e.Phase == "commentary"
}

func isNoiseEntry(e cc.Entry) bool {
	switch e.Type {
	case "reasoning", "event_msg", "turn_context":
		return true
	default:
		return false
	}
}

func (f *Formatter) omitEntryByMode(e cc.Entry) bool {
	if f.opts.Tools == "omit" && e.Message != nil && e.Type == "assistant" && len(e.Message.ToolUses()) > 0 && f.entryBody(e) == "" {
		return true
	}
	if f.opts.ToolResults == "omit" && isToolResultEntry(e) {
		return true
	}
	return false
}

func (f *Formatter) toolCommand(block cc.ContentBlock) string {
	var input struct {
		Cmd     string `json:"cmd"`
		Command string `json:"command"`
		Query   string `json:"query"`
	}
	if err := json.Unmarshal(block.Input, &input); err != nil {
		return ""
	}
	switch {
	case strings.TrimSpace(input.Cmd) != "":
		return f.redactText(strings.TrimSpace(input.Cmd))
	case strings.TrimSpace(input.Command) != "":
		return f.redactText(strings.TrimSpace(input.Command))
	case strings.TrimSpace(input.Query) != "":
		return f.redactText(strings.TrimSpace(input.Query))
	default:
		return ""
	}
}

func (f *Formatter) toolBlock(block cc.ContentBlock) string {
	cmd := f.toolCommand(block)
	if cmd != "" {
		return cmd
	}
	if len(block.Input) == 0 {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, block.Input, "", "  "); err == nil {
		return f.redactText(out.String())
	}
	return f.redactText(string(block.Input))
}

func (f *Formatter) textToolLine(block cc.ContentBlock) string {
	if s := toolSummary(block); s != "" {
		return "tool " + block.Name + ": " + f.limitText(s)
	}
	line := "tool " + block.Name
	if cmd := f.toolCommand(block); cmd != "" {
		line += ": " + f.limitText(cmd)
	}
	return line
}

func (f *Formatter) markdownToolLine(block cc.ContentBlock) string {
	if s := toolSummary(block); s != "" {
		return fmt.Sprintf("Tool `%s`: `%s`", block.Name, s)
	}
	if cmd := f.toolCommand(block); cmd != "" {
		return fmt.Sprintf("Tool `%s`: `%s`", block.Name, cmd)
	}
	return fmt.Sprintf("Tool `%s`", block.Name)
}

func toolSummary(block cc.ContentBlock) string {
	switch block.Name {
	case "Workflow":
		var input struct {
			Script     string `json:"script"`
			ScriptPath string `json:"scriptPath"`
		}
		if json.Unmarshal(block.Input, &input) != nil {
			return ""
		}
		if name := workflowMetaField(input.Script, "name"); name != "" {
			return name
		}
		return input.ScriptPath
	case "TaskCreate":
		var input struct {
			Subject string `json:"subject"`
		}
		if json.Unmarshal(block.Input, &input) == nil {
			return input.Subject
		}
	case "TaskUpdate":
		var input struct {
			TaskID string `json:"taskId"`
			Status string `json:"status"`
		}
		if json.Unmarshal(block.Input, &input) == nil {
			return "#" + input.TaskID + " " + input.Status
		}
	}
	return ""
}

func workflowMetaField(script, field string) string {
	re := regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(field) + `\s*:\s*['"]([^'"]+)['"]`)
	m := re.FindStringSubmatch(script)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func (f *Formatter) toolResultText(block cc.ContentBlock) string {
	body := f.redactText(strings.TrimSpace(block.Content))
	if body == "" {
		return ""
	}
	if f.opts.ToolResults == "summary" {
		return f.summarizeToolResult(body)
	}
	return f.limitText(body)
}

type toolResultItem struct {
	Text      string
	ImageURL  string
	Path      string
	MIMEType  string
	MediaType string
}

func (f *Formatter) parseToolResultItems(block cc.ContentBlock) []toolResultItem {
	body := strings.TrimSpace(block.Content)
	if body == "" {
		return nil
	}

	var arr []map[string]any
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		return nil
	}

	items := make([]toolResultItem, 0, len(arr))
	for _, raw := range arr {
		item := toolResultItem{}
		if s, _ := raw["text"].(string); s != "" {
			item.Text = f.redactText(s)
		}
		if s, _ := raw["image_url"].(string); s != "" {
			item.ImageURL = s
		}
		if s, _ := raw["path"].(string); s != "" {
			item.Path = s
		}
		if s, _ := raw["file_path"].(string); s != "" {
			item.Path = s
		}
		if s, _ := raw["mime_type"].(string); s != "" {
			item.MIMEType = s
		}
		if s, _ := raw["media_type"].(string); s != "" {
			item.MediaType = s
		}
		if item.Text != "" || item.ImageURL != "" || item.Path != "" {
			items = append(items, item)
		}
	}
	return items
}

func (f *Formatter) writeMarkdownToolResult(w io.Writer, block cc.ContentBlock) error {
	if f.opts.ToolResults == "summary" {
		body := f.toolResultText(block)
		if strings.TrimSpace(body) == "" {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s\n\n", normalizeParagraphs(body))
		return err
	}

	items := f.parseToolResultItems(block)
	if len(items) == 0 {
		body := f.toolResultText(block)
		if strings.TrimSpace(body) == "" {
			return nil
		}
		if err := writeFence(w, "text", body); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w)
		return err
	}

	var textParts []string
	for _, item := range items {
		if strings.TrimSpace(item.Text) != "" {
			textParts = append(textParts, strings.TrimSpace(item.Text))
		}
	}
	if len(textParts) > 0 {
		if err := writeFence(w, "text", f.limitText(strings.Join(textParts, "\n"))); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	for i, item := range items {
		ref, err := f.markdownImageRef(cc.ContentBlock{
			ImageURL:  item.ImageURL,
			Path:      item.Path,
			MIMEType:  item.MIMEType,
			MediaType: item.MediaType,
		})
		if err != nil || ref == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "![tool-result-image-%d](%s)\n\n", i+1, ref); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) summarizeToolResult(body string) string {
	body = f.redactText(body)
	if summary := summarizePatchedFiles(body); summary != "" {
		return summary
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 {
		return ""
	}
	command := ""
	status := ""
	total := ""
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "Command: "):
			command = strings.TrimPrefix(line, "Command: ")
		case strings.HasPrefix(line, "Process exited with code "):
			status = line
		case strings.HasPrefix(line, "Total output lines: "):
			total = line
		}
	}
	var parts []string
	if command != "" {
		parts = append(parts, command)
	}
	if status != "" {
		parts = append(parts, status)
	}
	if total != "" {
		parts = append(parts, total)
	}
	if len(parts) == 0 {
		if len(lines) > 3 {
			lines = lines[:3]
		}
		return f.limitText(strings.Join(lines, "\n"))
	}
	return strings.Join(parts, " | ")
}

func summarizePatchedFiles(body string) string {
	const marker = "Updated the following files:"
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	lines := strings.Split(body[idx+len(marker):], "\n")
	var files []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		status := fields[0]
		if len(status) > 2 {
			break
		}
		files = append(files, strings.Join(fields[1:], " "))
	}
	if len(files) == 0 {
		return ""
	}
	return "Updated files: " + strings.Join(files, ", ")
}

func (f *Formatter) limitText(s string) string {
	s = f.redactText(strings.TrimSpace(s))
	if f.opts.MaxBytes <= 0 || len(s) <= f.opts.MaxBytes {
		return s
	}
	cut := s[:f.opts.MaxBytes]
	if idx := strings.LastIndex(cut, "\n"); idx >= f.opts.MaxBytes/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, "\n") + "\n\n[truncated]"
}

func (f *Formatter) redactText(s string) string {
	if !f.opts.Redact || s == "" {
		return s
	}
	for _, rule := range defaultRedactions {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}

func (f *Formatter) applyCleanupPreset() {
	switch f.opts.Cleanup {
	case "", "none":
		return
	case "publish":
		if !f.opts.Explicit["tools"] {
			f.opts.Tools = "summary"
		}
		if !f.opts.Explicit["tool-results"] {
			f.opts.ToolResults = "summary"
		}
		if !f.opts.Explicit["max-bytes"] && f.opts.MaxBytes == 4000 {
			f.opts.MaxBytes = 1200
		}
	case "digest":
		if !f.opts.Explicit["tools"] {
			f.opts.Tools = "summary"
		}
		if !f.opts.Explicit["tool-results"] {
			f.opts.ToolResults = "full"
		}
		if !f.opts.Explicit["max-bytes"] && f.opts.MaxBytes == 4000 {
			f.opts.MaxBytes = 8000
		}
	default:
	}
}

func (f *Formatter) validateModes() error {
	if err := validateChoice("cleanup", f.opts.Cleanup, "none", "publish", "digest"); err != nil {
		return err
	}
	if err := validateChoice("tools", f.opts.Tools, "full", "summary", "omit"); err != nil {
		return err
	}
	if err := validateChoice("tool-results", f.opts.ToolResults, "full", "summary", "omit"); err != nil {
		return err
	}
	return nil
}

func validateChoice(name, got string, vals ...string) error {
	for _, v := range vals {
		if got == v {
			return nil
		}
	}
	return fmt.Errorf("invalid %s %q", name, got)
}

func toolLanguage(block cc.ContentBlock) string {
	switch block.Name {
	case "Bash":
		return "sh"
	case "WebSearch":
		return "text"
	default:
		return "json"
	}
}

func normalizeParagraphs(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
}

func writeFence(w io.Writer, lang, body string) error {
	fence := markdownFence(body)
	if lang != "" {
		if _, err := fmt.Fprintf(w, "%s%s\n", fence, lang); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(w, "%s\n", fence); err != nil {
			return err
		}
	}
	if body != "" {
		if _, err := fmt.Fprintln(w, body); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%s\n", fence)
	return err
}

func writeMarkdownTranscriptBlock(w io.Writer, body string) error {
	body = normalizeParagraphs(body)
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if _, err := fmt.Fprintln(w, ">"); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "> %s\n", line); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (f *Formatter) writeMarkdownImages(w io.Writer, e cc.Entry) error {
	if f.opts.Format != "markdown" && f.opts.Format != "md" {
		return nil
	}
	for i, block := range entryImages(e) {
		ref, err := f.markdownImageRef(block)
		if err != nil || ref == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "![image-%d](%s)\n\n", i+1, ref); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) markdownImageRef(block cc.ContentBlock) (string, error) {
	if !f.opts.InlineImages {
		if block.ImageURL != "" {
			return block.ImageURL, nil
		}
		if block.URL != "" {
			return block.URL, nil
		}
		if p := imagePath(block); p != "" {
			return p, nil
		}
	}
	if block.Data != "" {
		mimeType := block.MIMEType
		if mimeType == "" {
			mimeType = block.MediaType
		}
		if mimeType == "" {
			mimeType = "image/png"
		}
		data, err := base64.StdEncoding.DecodeString(block.Data)
		if err != nil {
			return "", err
		}
		data, mimeType = f.maybeRedactInlineImage(data, mimeType)
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if p := imagePath(block); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		mimeType := imageMIMEType(p, data, block)
		data, mimeType = f.maybeRedactInlineImage(data, mimeType)
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if block.ImageURL != "" {
		if data, mimeType, ok := parseDataImageURL(block.ImageURL); ok {
			data, mimeType = f.maybeRedactInlineImage(data, mimeType)
			return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
		}
		return block.ImageURL, nil
	}
	if block.URL != "" {
		return block.URL, nil
	}
	return "", nil
}

func (f *Formatter) maybeRedactInlineImage(data []byte, mimeType string) ([]byte, string) {
	if !f.opts.RedactImages || len(data) == 0 {
		return data, mimeType
	}
	redacted, err := redactImageDataFunc(data)
	if err != nil || len(redacted) == 0 {
		return data, mimeType
	}
	return redacted, "image/png"
}

func parseDataImageURL(s string) ([]byte, string, bool) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return nil, "", false
	}
	rest := strings.TrimPrefix(s, prefix)
	semi := strings.IndexByte(rest, ';')
	comma := strings.IndexByte(rest, ',')
	if semi < 0 || comma < 0 || semi > comma {
		return nil, "", false
	}
	if rest[semi:comma] != ";base64" {
		return nil, "", false
	}
	data, err := base64.StdEncoding.DecodeString(rest[comma+1:])
	if err != nil {
		return nil, "", false
	}
	return data, rest[:semi], true
}

// redactImageData is a no-op by default. Tests may override
// redactImageDataFunc to inject behavior.
func redactImageData(data []byte) ([]byte, error) {
	return data, nil
}

func imagePath(block cc.ContentBlock) string {
	if block.Path != "" {
		return block.Path
	}
	return block.FilePath
}

func imageMIMEType(path string, data []byte, block cc.ContentBlock) string {
	if block.MIMEType != "" {
		return block.MIMEType
	}
	if block.MediaType != "" {
		return block.MediaType
	}
	if ext := filepath.Ext(path); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "image/png"
}

func markdownFence(body string) string {
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	return fence
}
