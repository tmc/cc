package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/apple/x/vzkit/ocr"
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
	redactImagesFlag = flag.Bool("redact-images", true, "Redact sensitive text from inline images in markdown when possible")
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
		entries, err := cc.ReadFile(args[0])
		return entries, args[0], err
	}

	var all []cc.Entry
	for _, path := range args {
		entries, err := cc.ReadFile(path)
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
	title     string
	sessionID string
	cwd       string
	started   time.Time
}

func summarize(entries []cc.Entry) sessionMeta {
	var meta sessionMeta
	for _, e := range entries {
		if meta.sessionID == "" && e.SessionID != "" {
			meta.sessionID = e.SessionID
		}
		if meta.cwd == "" && e.CWD != "" {
			meta.cwd = e.CWD
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
		return "compact summary"
	case e.Type == "system" && e.Subtype == "compact_boundary":
		return "compacted"
	case isToolResultEntry(e):
		return "tool result"
	case e.Message != nil && e.Message.Role != "":
		if e.Phase != "" {
			return e.Message.Role + " " + e.Phase
		}
		return e.Message.Role
	default:
		if e.Phase != "" {
			return e.Type + " " + e.Phase
		}
		return e.Type
	}
}

func markdownLabel(e cc.Entry) string {
	switch {
	case e.IsCompactSummary:
		return "Compact Summary"
	case e.Type == "system" && e.Subtype == "compact_boundary":
		return "Compacted"
	case isToolResultEntry(e):
		return "Tool Result"
	case e.Message != nil && e.Message.Role != "":
		label := strings.Title(e.Message.Role)
		if e.Phase != "" {
			label += " (" + e.Phase + ")"
		}
		return label
	default:
		label := strings.Title(e.Type)
		if e.Phase != "" {
			label += " (" + e.Phase + ")"
		}
		return label
	}
}

func entryBody(e cc.Entry) string {
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
	line := "tool " + block.Name
	if cmd := toolCommand(block); cmd != "" {
		line += ": " + limitText(cmd)
	}
	return line
}

func markdownToolLine(block cc.ContentBlock) string {
	if cmd := toolCommand(block); cmd != "" {
		return fmt.Sprintf("Tool `%s`: `%s`", block.Name, cmd)
	}
	return fmt.Sprintf("Tool `%s`", block.Name)
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

func redactImageData(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	svc := ocr.NewService(false)
	obs, err := svc.RecognizeText(img)
	if err != nil {
		return nil, err
	}
	rects := sensitiveObservationRects(obs, img.Bounds(), 8)
	if len(rects) == 0 {
		return data, nil
	}
	dst := cloneRGBAImage(img)
	for _, r := range mergeImageRects(rects) {
		applyBlurImageRedaction(dst, r, color.Black, 10)
	}
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func sensitiveObservationRects(observations []ocr.TextObservation, bounds image.Rectangle, padding int) []image.Rectangle {
	var rects []image.Rectangle
	for _, obs := range observations {
		if !looksSensitiveText(obs.Text) {
			continue
		}
		rects = append(rects, paddedImageRect(observationImageRect(obs, bounds), bounds, padding))
	}
	return rects
}

func looksSensitiveText(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z':
			b.WriteRune(r)
		case '0' <= r && r <= '9':
			b.WriteRune(r)
		}
	}
	norm := b.String()
	return strings.Contains(norm, "pktest") || strings.Contains(norm, "sktest")
}

func observationImageRect(obs ocr.TextObservation, bounds image.Rectangle) image.Rectangle {
	w, h := bounds.Dx(), bounds.Dy()
	x1 := int(obs.BoundingBox.Origin.X * float64(w))
	y1 := int((1 - obs.BoundingBox.Origin.Y - obs.BoundingBox.Size.Height) * float64(h))
	x2 := int((obs.BoundingBox.Origin.X + obs.BoundingBox.Size.Width) * float64(w))
	y2 := int((1 - obs.BoundingBox.Origin.Y) * float64(h))
	return image.Rect(x1, y1, x2, y2).Canon()
}

func paddedImageRect(r, bounds image.Rectangle, padding int) image.Rectangle {
	if padding < 0 {
		padding = 0
	}
	r = image.Rect(r.Min.X-padding, r.Min.Y-padding, r.Max.X+padding, r.Max.Y+padding)
	return r.Intersect(bounds)
}

func mergeImageRects(rects []image.Rectangle) []image.Rectangle {
	if len(rects) < 2 {
		return rects
	}
	merged := append([]image.Rectangle(nil), rects...)
	changed := true
	for changed {
		changed = false
		var next []image.Rectangle
		for len(merged) > 0 {
			r := merged[0]
			merged = merged[1:]
			i := 0
			for i < len(merged) {
				if imageRectsTouch(r, merged[i]) {
					r = r.Union(merged[i])
					merged = append(merged[:i], merged[i+1:]...)
					changed = true
					continue
				}
				i++
			}
			next = append(next, r)
		}
		merged = next
	}
	return merged
}

func imageRectsTouch(a, b image.Rectangle) bool {
	return a.Inset(-1).Overlaps(b.Inset(-1))
}

func cloneRGBAImage(src image.Image) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}

func applyBlurImageRedaction(dst *image.RGBA, r image.Rectangle, fill color.Color, blur int) {
	if blur < 1 {
		blur = 1
	}
	src := cloneImageRegion(dst, r)
	pixelateImage(src, blur)
	boxBlurImage(src, blur)
	draw.Draw(dst, r, src, src.Bounds().Min, draw.Src)
	applyImageTint(dst, r, fill, 80)
}

func cloneImageRegion(src *image.RGBA, r image.Rectangle) *image.RGBA {
	sub := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(sub, sub.Bounds(), src, r.Min, draw.Src)
	return sub
}

func pixelateImage(img *image.RGBA, size int) {
	if size < 1 {
		size = 1
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += size {
		for x := b.Min.X; x < b.Max.X; x += size {
			block := image.Rect(x, y, minInt(x+size, b.Max.X), minInt(y+size, b.Max.Y))
			c := averageImageColor(img, block)
			draw.Draw(img, block, &image.Uniform{C: c}, image.Point{}, draw.Src)
		}
	}
}

func boxBlurImage(img *image.RGBA, radius int) {
	if radius < 1 {
		return
	}
	src := cloneRGBAImage(img)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			block := image.Rect(maxInt(x-radius, b.Min.X), maxInt(y-radius, b.Min.Y), minInt(x+radius+1, b.Max.X), minInt(y+radius+1, b.Max.Y))
			img.Set(x, y, averageImageColor(src, block))
		}
	}
}

func averageImageColor(img image.Image, r image.Rectangle) color.RGBA {
	if r.Empty() {
		return color.RGBA{}
	}
	var rs, gs, bs, as uint64
	var n uint64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			rr, gg, bb, aa := img.At(x, y).RGBA()
			rs += uint64(rr >> 8)
			gs += uint64(gg >> 8)
			bs += uint64(bb >> 8)
			as += uint64(aa >> 8)
			n++
		}
	}
	return color.RGBA{
		R: uint8(rs / n),
		G: uint8(gs / n),
		B: uint8(bs / n),
		A: uint8(as / n),
	}
}

func applyImageTint(dst *image.RGBA, r image.Rectangle, fill color.Color, alpha uint8) {
	rr, gg, bb, _ := fill.RGBA()
	mask := color.RGBA{R: uint8(rr >> 8), G: uint8(gg >> 8), B: uint8(bb >> 8), A: alpha}
	draw.DrawMask(dst, r, &image.Uniform{C: mask}, image.Point{}, &image.Uniform{C: color.Alpha{A: alpha}}, image.Point{}, draw.Over)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
