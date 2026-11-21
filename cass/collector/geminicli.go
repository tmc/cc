package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/ccusage"
	"github.com/tmc/cc/internal/geminiread"
)

// GeminiCLI collects sessions from Gemini CLI session files.
type GeminiCLI struct {
	// Root overrides auto-discovered Gemini roots.
	Root string
}

// Name returns the agent slug "gemini-cli".
func (c *GeminiCLI) Name() string { return "gemini-cli" }

// Detect reports whether Gemini CLI session data is present on the system.
func (c *GeminiCLI) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	paths, err := c.roots()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	if len(paths) == 0 {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: true,
		Paths: paths,
	}, nil
}

// Scan walks Gemini CLI session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *GeminiCLI) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		var err error
		paths, err = c.roots()
		if err != nil {
			return err
		}
	}

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if err := c.scanDir(ctx, root, config, out); err != nil {
				return err
			}
			continue
		}

		if !isGeminiSessionFile(root) {
			continue
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			continue
		}
		session, err := c.parseSession(ctx, config, root)
		if err != nil {
			continue
		}
		if config.Project != "" && !matchProject(session, config.Project) {
			continue
		}
		select {
		case out <- session:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *GeminiCLI) scanDir(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			switch info.Name() {
			case "subagents", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !isGeminiSessionFile(path) {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}

		session, err := c.parseSession(ctx, config, path)
		if err != nil {
			return nil
		}
		if config.Project != "" && !matchProject(session, config.Project) {
			return nil
		}

		select {
		case out <- session:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

func matchProject(sess cass.Session, project string) bool {
	q := strings.ToLower(project)
	return strings.Contains(strings.ToLower(sess.Workspace), q) ||
		strings.Contains(strings.ToLower(sess.SourcePath), q) ||
		strings.Contains(strings.ToLower(sess.Title), q)
}

func isGeminiSessionFile(path string) bool {
	if strings.HasSuffix(path, ".jsonl") {
		return true
	}
	if strings.HasSuffix(path, ".json") && strings.HasPrefix(filepath.Base(path), "session-") {
		return strings.Contains(path, string(filepath.Separator)+"chats"+string(filepath.Separator))
	}
	return false
}

func (c *GeminiCLI) parseSession(ctx context.Context, config cass.ScanConfig, path string) (cass.Session, error) {
	entries, workspace, err := c.readEntries(ctx, config, path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	sum := cc.Summarize(path, entries)

	// Build normalized messages.
	var messages []cass.Message
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := e.Message.TextContent()
		if text == "" {
			continue
		}
		msg := cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		}
		messages = append(messages, msg)
	}

	if workspace == "" {
		workspace = workspaceFromPath(path)
	}

	id := sessionID(path)

	links := ExtractLinks(entries)
	stats := ExtractStats(entries)

	teamLinks := ExtractTeamLinks(entries)
	links = append(links, teamLinks...)

	meta := map[string]any{}
	if sum.GitBranch != "" {
		meta["git_branch"] = sum.GitBranch
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}

	itermSID := extractItermSessionID(entries)
	if itermSID != "" {
		meta["iterm_session"] = itermSID
	}

	teamName, agentName, isTeamLead := ClassifyTeamRole(entries)

	return cass.Session{
		ID:         id,
		Agent:      "gemini-cli",
		Title:      titleFromSummary(sum),
		Workspace:  workspace,
		SourcePath: path,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Stats:      stats,
		Usage:      cass.UsageEvents(ccusage.FromEntries(c.Name(), entries)),
		Skills:     ExtractSkills(entries, c.Name()),
		Metadata:   meta,
		TeamName:   teamName,
		AgentName:  agentName,
		IsTeamLead: isTeamLead,
	}, nil
}

func (c *GeminiCLI) readEntries(ctx context.Context, config cass.ScanConfig, path string) ([]cc.Entry, string, error) {
	if strings.HasSuffix(path, ".json") {
		entries, err := geminiread.ReadFile(ctx, path)
		if err != nil {
			return nil, "", err
		}
		var workspace string
		if len(entries) > 0 {
			workspace = entries[0].CWD
			if entries[0].Type == "session_meta" {
				entries = entries[1:]
			}
		}
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].Timestamp.Before(entries[j].Timestamp)
		})
		return entries, workspace, nil
	}

	entries, err := readSessionFile(ctx, config, path)
	if err != nil {
		return nil, "", err
	}

	// Merge subagent entries from Claude-style layout, when present.
	subagentDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if infos, err := os.ReadDir(subagentDir); err == nil {
		for _, fi := range infos {
			name := fi.Name()
			if !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			if strings.HasPrefix(name, "agent-acompact") {
				continue
			}
			sub, err := readSessionFile(ctx, config, filepath.Join(subagentDir, name))
			if err == nil {
				entries = append(entries, sub...)
			}
		}
	}
	return entries, workspaceFromPath(path), nil
}

func (c *GeminiCLI) roots() ([]string, error) {
	if c.Root != "" {
		return []string{c.Root}, nil
	}

	gh, err := ccpaths.GeminiHome()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var roots []string
	addDir := func(p string) {
		if p == "" || seen[p] {
			return
		}
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			seen[p] = true
			roots = append(roots, p)
		}
	}

	addDir(filepath.Join(gh, "projects"))

	// Gemini CLI stores chats under ~/.gemini/tmp/<project>/chats/session-*.json.
	tmpRoot := filepath.Join(gh, "tmp")
	if entries, err := os.ReadDir(tmpRoot); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			addDir(filepath.Join(tmpRoot, e.Name(), "chats"))
		}
	}
	return roots, nil
}
