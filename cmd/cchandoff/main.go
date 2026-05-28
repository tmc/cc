// Command cchandoff builds a cross-tool handoff prompt from a prior session.
//
// It reads a Claude JSONL session or a Gemini chat JSON file and produces a
// structured bootstrap prompt for continuing work in the target tool.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

type handoffData struct {
	SourcePath      string   `json:"source_path"`
	SourceAgent     string   `json:"source_agent"`
	TargetAgent     string   `json:"target_agent"`
	LaunchCommand   string   `json:"launch_command,omitempty"`
	Workspace       string   `json:"workspace,omitempty"`
	Title           string   `json:"title,omitempty"`
	StartedAt       string   `json:"started_at,omitempty"`
	EndedAt         string   `json:"ended_at,omitempty"`
	FirstPrompt     string   `json:"first_prompt,omitempty"`
	LatestUserAsk   string   `json:"latest_user_ask,omitempty"`
	RecentContext   []string `json:"recent_context,omitempty"`
	FilesTouched    []string `json:"files_touched,omitempty"`
	RecentCommands  []string `json:"recent_commands,omitempty"`
	GitBranch       string   `json:"git_branch,omitempty"`
	GitStatusSample []string `json:"git_status_sample,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "cchandoff: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		fromPath   = flag.String("from", "", "Source session file (.jsonl or Gemini session-*.json)")
		toFlag     = flag.String("to", "gemini", "Target tool: gemini|gemini-cli|claude|claude-code|codex|codex-cli|codex-app")
		workspace  = flag.String("workspace", "", "Override workspace path")
		contextN   = flag.Int("context", 10, "Number of recent conversation lines to include")
		commandsN  = flag.Int("commands", 10, "Number of recent shell commands to include")
		filesN     = flag.Int("files", 20, "Maximum files to include")
		includeGit = flag.Bool("git", true, "Include git branch/status snapshot")
		jsonOut    = flag.Bool("json", false, "Output machine-readable JSON")
		outPath    = flag.String("out", "", "Write output to file instead of stdout")
	)
	flag.Parse()

	if *fromPath == "" && flag.NArg() > 0 {
		*fromPath = flag.Arg(0)
	}
	if *fromPath == "" {
		return errors.New("missing -from")
	}

	target, launchBin, err := normalizeTarget(*toFlag)
	if err != nil {
		return err
	}

	entries, err := readSessionEntries(*fromPath)
	if err != nil {
		return fmt.Errorf("read session: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("empty session: %s", *fromPath)
	}

	sum := cc.Summarize(*fromPath, entries)
	data := buildHandoff(*fromPath, inferSourceAgent(*fromPath, entries), target, launchBin, entries, sum, *contextN, *commandsN, *filesN)
	if *workspace != "" {
		data.Workspace = *workspace
	}
	if *includeGit && data.Workspace != "" {
		data.GitBranch, data.GitStatusSample = gitSnapshot(data.Workspace, 20)
	}

	var out string
	if *jsonOut {
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		out = string(b) + "\n"
	} else {
		out = renderPrompt(data)
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(out), 0o644); err != nil {
			return fmt.Errorf("write -out: %w", err)
		}
		return nil
	}
	fmt.Print(out)
	return nil
}

func normalizeTarget(v string) (targetAgent, launchBin string, err error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "gemini", "gemini-cli":
		return "gemini-cli", "gemini", nil
	case "claude", "claude-code":
		return "claude-code", "claude", nil
	case "codex", "codex-cli", "codex-app":
		return "codex-cli", "codex", nil
	default:
		return "", "", fmt.Errorf("unsupported -to %q (want gemini|gemini-cli|claude|claude-code|codex|codex-cli|codex-app)", v)
	}
}

func inferSourceAgent(path string, entries []cc.Entry) string {
	for _, e := range entries {
		switch {
		case e.Originator == "codex_cli_rs" || e.Source == "cli":
			return "codex-cli"
		case e.Originator == "Codex Desktop" || e.Source == "vscode":
			return "codex-app"
		}
	}

	p := strings.ToLower(path)
	switch {
	case strings.Contains(p, ".codex"):
		return "codex-cli"
	case strings.Contains(p, ".claude"):
		return "claude-code"
	case strings.Contains(p, ".gemini"):
		return "gemini-cli"
	default:
		return "unknown"
	}
}

func readSessionEntries(path string) ([]cc.Entry, error) {
	if strings.HasSuffix(path, ".jsonl") {
		return cc.ReadFile(context.Background(), path)
	}
	if strings.HasSuffix(path, ".json") && strings.HasPrefix(filepath.Base(path), "session-") {
		return readGeminiSessionJSON(path)
	}
	return nil, fmt.Errorf("unsupported session file format: %s", path)
}

func readGeminiSessionJSON(path string) ([]cc.Entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sess struct {
		SessionID string `json:"sessionId"`
		Messages  []struct {
			ID        string          `json:"id"`
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Content   json.RawMessage `json:"content"`
			Model     string          `json:"model"`
			Tokens    struct {
				Input  int `json:"input"`
				Output int `json:"output"`
				Cached int `json:"cached"`
			} `json:"tokens"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &sess); err != nil {
		return nil, err
	}

	entries := make([]cc.Entry, 0, len(sess.Messages))
	for i, m := range sess.Messages {
		if cc.ExtractAnyText(m.Content) == "" {
			continue
		}
		role := "assistant"
		if m.Type == "user" {
			role = "user"
		}
		ts, _ := time.Parse(time.RFC3339, m.Timestamp)
		id := m.ID
		if id == "" {
			id = fmt.Sprintf("m-%d", i+1)
		}
		e := cc.Entry{
			Type:      "message",
			SessionID: sess.SessionID,
			UUID:      id,
			Timestamp: ts,
			Message: &cc.Message{
				ID:      id,
				Role:    role,
				Content: m.Content,
				Model:   m.Model,
			},
		}
		if role == "assistant" {
			e.Message.Usage = &cc.Usage{
				InputTokens:          m.Tokens.Input,
				OutputTokens:         m.Tokens.Output,
				CacheReadInputTokens: m.Tokens.Cached,
			}
		}
		entries = append(entries, e)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

func buildHandoff(sourcePath, sourceAgent, targetAgent, launchBin string, entries []cc.Entry, sum cc.SessionSummary, contextN, commandsN, filesN int) handoffData {
	d := handoffData{
		SourcePath:    sourcePath,
		SourceAgent:   sourceAgent,
		TargetAgent:   targetAgent,
		LaunchCommand: launchBin,
		Workspace:     sum.CWD,
		Title:         handoffTitle(sum, sourcePath),
	}
	if !sum.FirstTime.IsZero() {
		d.StartedAt = sum.FirstTime.Format(time.RFC3339)
	}
	if !sum.LastTime.IsZero() {
		d.EndedAt = sum.LastTime.Format(time.RFC3339)
	}
	if sum.FirstPrompt != "" {
		d.FirstPrompt = strings.TrimSpace(sum.FirstPrompt)
	}

	var ctx []string
	var userMsgs []string
	files := dedupList{}
	commands := dedupList{}

	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		blocks := e.Message.ContentBlocks()
		text := strings.TrimSpace(e.Message.TextContent())
		role := strings.ToUpper(e.Message.Role)
		switch e.Message.Role {
		case "user":
			// Tool-result-only user turns are usually shell output echoes, not intent.
			if len(e.Message.ToolResults()) > 0 && !hasTextBlocks(blocks) {
				continue
			}
			if isNonIntentUserText(text) {
				continue
			}
			if text != "" {
				userMsgs = append(userMsgs, text)
				ctx = append(ctx, "USER: "+truncateLine(text, 320))
			}
		case "assistant":
			if text != "" {
				ctx = append(ctx, "ASSISTANT: "+truncateLine(text, 320))
			}
		}

		for _, tu := range e.Message.ToolUses() {
			switch tu.Name {
			case "Write", "Edit", "MultiEdit":
				if fp := fieldString(tu.Input, "file_path", "filePath", "path"); fp != "" {
					files.Add(fp)
				}
			case "Bash":
				if cmd := fieldString(tu.Input, "command"); cmd != "" {
					commands.Add(truncateLine(cmd, 220))
				}
			}
		}

		// Handle sessions that use plain text tool/event style in user turns.
		if role == "USER" && strings.HasPrefix(strings.ToLower(text), "error:") {
			ctx = append(ctx, "ERROR: "+truncateLine(text, 320))
		}
	}

	if len(userMsgs) > 0 {
		d.LatestUserAsk = userMsgs[len(userMsgs)-1]
	}
	d.RecentContext = tail(ctx, max(1, contextN))
	d.RecentCommands = tail(commands.Items(), max(1, commandsN))
	d.FilesTouched = head(files.Items(), max(1, filesN))
	return d
}

func hasTextBlocks(blocks []cc.ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

func isNonIntentUserText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	l := strings.ToLower(s)
	if strings.HasPrefix(l, "[request interrupted by user") {
		return true
	}
	if strings.HasPrefix(l, "successfully ") && strings.Contains(l, " it2") {
		return true
	}
	return false
}

func renderPrompt(d handoffData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Cross-Tool Handoff\n\n")
	fmt.Fprintf(&b, "- Source: `%s` (%s)\n", d.SourceAgent, d.SourcePath)
	fmt.Fprintf(&b, "- Target: `%s`\n", d.TargetAgent)
	if d.Workspace != "" {
		fmt.Fprintf(&b, "- Workspace: `%s`\n", d.Workspace)
	}
	if d.StartedAt != "" || d.EndedAt != "" {
		fmt.Fprintf(&b, "- Session Time: `%s` -> `%s`\n", d.StartedAt, d.EndedAt)
	}
	if d.GitBranch != "" {
		fmt.Fprintf(&b, "- Git Branch: `%s`\n", d.GitBranch)
	}
	if d.Workspace != "" && d.LaunchCommand != "" {
		fmt.Fprintf(&b, "- Launch: `cd %s && %s`\n", d.Workspace, d.LaunchCommand)
	}

	fmt.Fprintf(&b, "\n## Bootstrap Prompt\n\n")
	fmt.Fprintf(&b, "Continue an in-progress coding task handed off from `%s` to `%s`.\n", d.SourceAgent, d.TargetAgent)
	if d.Title != "" {
		fmt.Fprintf(&b, "Original session title: %s\n", d.Title)
	}
	if d.FirstPrompt != "" {
		fmt.Fprintf(&b, "Initial user intent: %s\n", d.FirstPrompt)
	}
	if d.LatestUserAsk != "" {
		fmt.Fprintf(&b, "Latest user request: %s\n", d.LatestUserAsk)
	}

	if len(d.FilesTouched) > 0 {
		fmt.Fprintf(&b, "\nLikely touched files:\n")
		for _, f := range d.FilesTouched {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	if len(d.RecentCommands) > 0 {
		fmt.Fprintf(&b, "\nRecent shell commands:\n")
		for _, c := range d.RecentCommands {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(d.RecentContext) > 0 {
		fmt.Fprintf(&b, "\nRecent conversation context:\n")
		for _, c := range d.RecentContext {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	if len(d.GitStatusSample) > 0 {
		fmt.Fprintf(&b, "\nRepository status snapshot:\n")
		for _, ln := range d.GitStatusSample {
			fmt.Fprintf(&b, "- %s\n", ln)
		}
	}

	fmt.Fprintf(&b, "\nHandoff instructions:\n")
	fmt.Fprintf(&b, "1. Restate the goal in one sentence.\n")
	fmt.Fprintf(&b, "2. Verify repository state before editing.\n")
	fmt.Fprintf(&b, "3. Continue from latest intent; do not restart from scratch.\n")
	fmt.Fprintf(&b, "4. Flag assumptions or missing context explicitly.\n")
	return b.String()
}

func handoffTitle(sum cc.SessionSummary, sourcePath string) string {
	if sum.CustomTitle != "" {
		return sum.CustomTitle
	}
	if sum.FirstPrompt != "" {
		return truncateLine(sum.FirstPrompt, 90)
	}
	return filepath.Base(sourcePath)
}

func gitSnapshot(workspace string, maxLines int) (branch string, status []string) {
	branch = strings.TrimSpace(runOutput("git", "-C", workspace, "rev-parse", "--abbrev-ref", "HEAD"))
	raw := strings.TrimSpace(runOutput("git", "-C", workspace, "status", "--short", "--branch"))
	if raw == "" {
		return branch, nil
	}
	for _, ln := range strings.Split(raw, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		status = append(status, ln)
		if len(status) >= maxLines {
			break
		}
	}
	return branch, status
}

func runOutput(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(b)
}

func fieldString(raw json.RawMessage, keys ...string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

type dedupList struct {
	items []string
	seen  map[string]bool
}

func (d *dedupList) Add(v string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return
	}
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if d.seen[v] {
		return
	}
	d.seen[v] = true
	d.items = append(d.items, v)
}

func (d *dedupList) Items() []string { return d.items }

func oneLine(s string) string { return strings.Join(strings.Fields(strings.TrimSpace(s)), " ") }

func truncateLine(s string, maxLen int) string {
	s = oneLine(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func tail(in []string, n int) []string {
	if n <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= n {
		return in
	}
	return in[len(in)-n:]
}

func head(in []string, n int) []string {
	if n <= 0 || len(in) == 0 {
		return nil
	}
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
