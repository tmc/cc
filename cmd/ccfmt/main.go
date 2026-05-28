package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	formatFlag       = flag.String("format", "text", "Output format: text, markdown, json, jsonl")
	titleFlag        = flag.String("title", "", "Optional document title")
	includeMeta      = flag.Bool("meta", false, "Include session metadata and injected preamble messages")
	includeStamp     = flag.Bool("timestamps", false, "Include timestamps in formatted output")
	maxBytesFlag     = flag.Int("max-bytes", 4000, "Maximum bytes to include from a single text or tool-result block; 0 means unlimited")
	cleanupFlag      = flag.String("cleanup", "none", "Cleanup preset: none, publish, digest")
	commentaryFlag   = flag.Bool("commentary", true, "Include commentary-phase assistant messages")
	toolsFlag        = flag.String("tools", "full", "Tool call rendering: full, summary, omit")
	toolResultsFlag  = flag.String("tool-results", "full", "Tool result rendering: full, summary, omit")
	inlineImagesFlag = flag.Bool("inline-images", true, "Inline message images as data URLs in markdown when possible")
	redactImagesFlag = flag.Bool("redact-images", false, "Redact sensitive text from inline images (requires an external redactor via redactImageDataFunc; default no-op)")
	redactFlag       = flag.Bool("redact", true, "Redact obvious secrets such as Stripe test keys")
)

var defaultRedactions = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\bpk_test_[A-Za-z0-9_]+\b`), "pk_test_XXXXX..."},
	{regexp.MustCompile(`\bsk_test_[A-Za-z0-9_]+\b`), "sk_test_XXXXX..."},
}

var redactImageDataFunc = redactImageData

func main() {
	flag.Parse()
	if err := run(os.Stdout, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "ccfmt: %v\n", err)
		os.Exit(1)
	}
}

func run(w io.Writer, args []string) error {
	applyCleanupPreset()
	if err := validateModes(); err != nil {
		return err
	}

	entries, source, err := readEntries(args)
	if err != nil {
		return err
	}

	switch *formatFlag {
	case "text":
		return writeText(w, source, entries)
	case "markdown", "md":
		return writeMarkdown(w, source, entries)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "jsonl":
		enc := json.NewEncoder(w)
		for _, e := range entries {
			if !shouldIncludeEntry(e) {
				continue
			}
			if err := enc.Encode(e); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q", *formatFlag)
	}
}

func readEntries(args []string) ([]cc.Entry, string, error) {
	if len(args) == 0 {
		entries, err := cc.ReadAll(os.Stdin)
		return entries, "stdin", err
	}
	if len(args) == 1 {
		entries, err := cc.ReadFileWithSubagents(args[0])
		return entries, args[0], err
	}

	var all []cc.Entry
	for _, path := range args {
		entries, err := cc.ReadFileWithSubagents(path)
		if err != nil {
			return nil, "", err
		}
		all = append(all, entries...)
	}
	return all, strings.Join(args, ", "), nil
}

func writeText(w io.Writer, source string, entries []cc.Entry) error {
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
		if !shouldIncludeEntry(e) {
			continue
		}
		if err := writeTextEntry(w, e); err != nil {
			return err
		}
	}
	return nil
}

func writeTextEntry(w io.Writer, e cc.Entry) error {
	label := textLabel(e)
	if *includeStamp && !e.Timestamp.IsZero() {
		label += " " + e.Timestamp.Local().Format("15:04:05")
	}
	fmt.Fprintf(w, "[%s]\n", label)

	switch {
	case e.Type == "system" && e.Subtype == "compact_boundary":
		_, err := fmt.Fprintln(w, "Conversation compacted.")
		if err != nil {
			return err
		}
	case e.IsCompactSummary:
		_, err := fmt.Fprintln(w, compactText(e))
		if err != nil {
			return err
		}
	case isToolResultEntry(e):
		if *toolResultsFlag == "omit" {
			return nil
		}
		for _, block := range e.Message.ToolResults() {
			body := toolResultText(block)
			if strings.TrimSpace(body) == "" {
				continue
			}
			if _, err := fmt.Fprintln(w, body); err != nil {
				return err
			}
		}
	default:
		text := entryBody(e)
		if text != "" {
			if _, err := fmt.Fprintln(w, limitText(text)); err != nil {
				return err
			}
		}
		if e.Message != nil && e.Type == "assistant" && *toolsFlag != "omit" {
			for _, tool := range e.Message.ToolUses() {
				fmt.Fprintln(w, textToolLine(tool))
			}
		}
	}

	_, err := fmt.Fprintln(w)
	return err
}

func writeMarkdown(w io.Writer, source string, entries []cc.Entry) error {
	meta := summarize(entries)
	title := *titleFlag
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
		if !shouldIncludeEntry(e) {
			continue
		}
		if err := writeMarkdownEntry(w, e); err != nil {
			return err
		}
	}
	return nil
}

func writeMarkdownEntry(w io.Writer, e cc.Entry) error {
	fmt.Fprintf(w, "## %s\n\n", markdownLabel(e))
	if *includeStamp && !e.Timestamp.IsZero() {
		fmt.Fprintf(w, "_%s_\n\n", e.Timestamp.Local().Format(time.RFC3339))
	}

	switch {
	case e.Type == "system" && e.Subtype == "compact_boundary":
		if _, err := fmt.Fprintln(w, "_Conversation compacted._"); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w)
		return err
	case e.IsCompactSummary:
		return writeFence(w, "", compactText(e))
	case isToolResultEntry(e):
		if *toolResultsFlag == "omit" {
			return nil
		}
		for _, block := range e.Message.ToolResults() {
			if err := writeMarkdownToolResult(w, block); err != nil {
				return err
			}
		}
		return nil
	default:
		text := entryBody(e)
		if text != "" {
			if err := writeMarkdownTranscriptBlock(w, limitText(text)); err != nil {
				return err
			}
		}
		if err := writeMarkdownImages(w, e); err != nil {
			return err
		}
		if e.Message != nil && e.Type == "assistant" && *toolsFlag != "omit" {
			for _, tool := range e.Message.ToolUses() {
				if *toolsFlag == "summary" {
					fmt.Fprintf(w, "- %s\n", markdownToolLine(tool))
					continue
				}
				fmt.Fprintf(w, "### Tool: `%s`\n\n", tool.Name)
				if err := writeFence(w, toolLanguage(tool), toolBlock(tool)); err != nil {
					return err
				}
				fmt.Fprintln(w)
			}
			if *toolsFlag == "summary" && len(e.Message.ToolUses()) > 0 {
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

func shouldIncludeEntry(e cc.Entry) bool {
	if isNoiseEntry(e) {
		return false
	}
	if *includeMeta {
		if !*commentaryFlag && isCommentary(e) {
			return false
		}
		return !omitEntryByMode(e)
	}
	if e.IsMeta {
		return false
	}
	if e.Type == "system" && e.Subtype == "session_meta" {
		return false
	}
	if !*commentaryFlag && isCommentary(e) {
		return false
	}
	return !omitEntryByMode(e)
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
		return decorateMarkdownLabel(strings.Title(e.Message.Role), e)
	default:
		return decorateMarkdownLabel(strings.Title(e.Type), e)
	}
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

func entryBody(e cc.Entry) string {
	if e.Attachment != nil {
		return attachmentText(e.Attachment)
	}
	if e.Message != nil {
		if blocks := e.Message.ContentBlocks(); blocks != nil {
			var parts []string
			for _, block := range blocks {
				if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
					parts = append(parts, redactText(strings.TrimSpace(block.Text)))
				}
			}
			return strings.Join(parts, "\n\n")
		}
		return redactText(strings.TrimSpace(e.Message.TextContent()))
	}
	if e.Summary != "" {
		return redactText(strings.TrimSpace(e.Summary))
	}
	if e.Content != "" {
		return redactText(strings.TrimSpace(e.Content))
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

func compactText(e cc.Entry) string {
	text := entryBody(e)
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

func omitEntryByMode(e cc.Entry) bool {
	if *toolsFlag == "omit" && e.Message != nil && e.Type == "assistant" && len(e.Message.ToolUses()) > 0 && entryBody(e) == "" {
		return true
	}
	if *toolResultsFlag == "omit" && isToolResultEntry(e) {
		return true
	}
	return false
}

func toolCommand(block cc.ContentBlock) string {
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
		return redactText(strings.TrimSpace(input.Cmd))
	case strings.TrimSpace(input.Command) != "":
		return redactText(strings.TrimSpace(input.Command))
	case strings.TrimSpace(input.Query) != "":
		return redactText(strings.TrimSpace(input.Query))
	default:
		return ""
	}
}

func toolBlock(block cc.ContentBlock) string {
	cmd := toolCommand(block)
	if cmd != "" {
		return cmd
	}
	if len(block.Input) == 0 {
		return ""
	}
	var out bytes.Buffer
	if err := json.Indent(&out, block.Input, "", "  "); err == nil {
		return redactText(out.String())
	}
	return redactText(string(block.Input))
}

func textToolLine(block cc.ContentBlock) string {
	if s := toolSummary(block); s != "" {
		return "tool " + block.Name + ": " + limitText(s)
	}
	line := "tool " + block.Name
	if cmd := toolCommand(block); cmd != "" {
		line += ": " + limitText(cmd)
	}
	return line
}

func markdownToolLine(block cc.ContentBlock) string {
	if s := toolSummary(block); s != "" {
		return fmt.Sprintf("Tool `%s`: `%s`", block.Name, s)
	}
	if cmd := toolCommand(block); cmd != "" {
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

func toolResultText(block cc.ContentBlock) string {
	body := redactText(strings.TrimSpace(block.Content))
	if body == "" {
		return ""
	}
	if *toolResultsFlag == "summary" {
		return summarizeToolResult(body)
	}
	return limitText(body)
}

type toolResultItem struct {
	Text      string
	ImageURL  string
	Path      string
	MIMEType  string
	MediaType string
}

func parseToolResultItems(block cc.ContentBlock) []toolResultItem {
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
			item.Text = redactText(s)
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

func writeMarkdownToolResult(w io.Writer, block cc.ContentBlock) error {
	if *toolResultsFlag == "summary" {
		body := toolResultText(block)
		if strings.TrimSpace(body) == "" {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s\n\n", normalizeParagraphs(body))
		return err
	}

	items := parseToolResultItems(block)
	if len(items) == 0 {
		body := toolResultText(block)
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
		if err := writeFence(w, "text", limitText(strings.Join(textParts, "\n"))); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	for i, item := range items {
		ref, err := markdownImageRef(cc.ContentBlock{
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

func summarizeToolResult(body string) string {
	body = redactText(body)
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
		return limitText(strings.Join(lines, "\n"))
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

func limitText(s string) string {
	s = redactText(strings.TrimSpace(s))
	if *maxBytesFlag <= 0 || len(s) <= *maxBytesFlag {
		return s
	}
	cut := s[:*maxBytesFlag]
	if idx := strings.LastIndex(cut, "\n"); idx >= *maxBytesFlag/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, "\n") + "\n\n[truncated]"
}

func redactText(s string) string {
	if !*redactFlag || s == "" {
		return s
	}
	for _, rule := range defaultRedactions {
		s = rule.re.ReplaceAllString(s, rule.repl)
	}
	return s
}

func applyCleanupPreset() {
	switch *cleanupFlag {
	case "", "none":
		return
	case "publish":
		if !flagWasSet("tools") {
			*toolsFlag = "summary"
		}
		if !flagWasSet("tool-results") {
			*toolResultsFlag = "summary"
		}
		if !flagWasSet("max-bytes") && *maxBytesFlag == 4000 {
			*maxBytesFlag = 1200
		}
	case "digest":
		if !flagWasSet("tools") {
			*toolsFlag = "summary"
		}
		if !flagWasSet("tool-results") {
			*toolResultsFlag = "full"
		}
		if !flagWasSet("max-bytes") && *maxBytesFlag == 4000 {
			*maxBytesFlag = 8000
		}
	default:
	}
}

func validateModes() error {
	if err := validateChoice("cleanup", *cleanupFlag, "none", "publish", "digest"); err != nil {
		return err
	}
	if err := validateChoice("tools", *toolsFlag, "full", "summary", "omit"); err != nil {
		return err
	}
	if err := validateChoice("tool-results", *toolResultsFlag, "full", "summary", "omit"); err != nil {
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

func flagWasSet(name string) bool {
	prefix := "-" + name
	for _, arg := range os.Args[1:] {
		if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
			return true
		}
	}
	return false
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

func writeMarkdownImages(w io.Writer, e cc.Entry) error {
	if *formatFlag != "markdown" && *formatFlag != "md" {
		return nil
	}
	for i, block := range entryImages(e) {
		ref, err := markdownImageRef(block)
		if err != nil || ref == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "![image-%d](%s)\n\n", i+1, ref); err != nil {
			return err
		}
	}
	return nil
}

func markdownImageRef(block cc.ContentBlock) (string, error) {
	if !*inlineImagesFlag {
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
		data, mimeType = maybeRedactInlineImage(data, mimeType)
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if p := imagePath(block); p != "" {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		mimeType := imageMIMEType(p, data, block)
		data, mimeType = maybeRedactInlineImage(data, mimeType)
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if block.ImageURL != "" {
		if data, mimeType, ok := parseDataImageURL(block.ImageURL); ok {
			data, mimeType = maybeRedactInlineImage(data, mimeType)
			return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
		}
		return block.ImageURL, nil
	}
	if block.URL != "" {
		return block.URL, nil
	}
	return "", nil
}

func maybeRedactInlineImage(data []byte, mimeType string) ([]byte, string) {
	if !*redactImagesFlag || len(data) == 0 {
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

// redactImageData is a no-op by default. OCR-based redaction lived here
// until 2026-04-20; it was removed along with the tmc/apple/x/vzkit/ocr
// dependency that bloated the ccfmt binary by ~10 MB. Callers wanting
// image redaction should pipe through an external redactor (for
// example, imgredact in tmc/misc) before or after ccfmt. Tests may
// override redactImageDataFunc to inject behavior.
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
