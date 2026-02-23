package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// GeminiCLI collects sessions from Gemini CLI's JSONL session files.
type GeminiCLI struct {
	// Root overrides the default ~/.gemini/projects directory.
	Root string
}

func (c *GeminiCLI) Name() string { return "gemini-cli" }

func (c *GeminiCLI) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	root, err := c.root()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: true,
		Paths: []string{root},
	}, nil
}

func (c *GeminiCLI) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		root, err := c.root()
		if err != nil {
			return err
		}
		paths = []string{root}
	}

	for _, root := range paths {
		if err := c.scanDir(ctx, root, config, out); err != nil {
			return err
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
		// Skip subagents/ directories during the Walk: subagent entries are
		// merged into their parent session by parseSession instead of being
		// emitted as separate sessions (they share the parent's sessionId).
		if info.IsDir() && info.Name() == "subagents" {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}
		if config.Project != "" {
			rel, _ := filepath.Rel(root, path)
			if !strings.Contains(strings.ToLower(rel), strings.ToLower(config.Project)) {
				return nil
			}
		}

		session, err := c.parseSession(path)
		if err != nil {
			return nil // skip unparseable files
		}

		select {
		case out <- session:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

func (c *GeminiCLI) parseSession(path string) (cass.Session, error) {
	entries, err := cc.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	// Merge subagent entries. Subagent files live at:
	//   <parent-dir>/<parent-uuid>/subagents/agent-<agentId>.jsonl
	// They share the parent's sessionId, so their tokens accumulate correctly.
	// Exclude acompact-* files (compaction subagents that duplicate history).
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
			sub, err := cc.ReadFile(filepath.Join(subagentDir, name))
			if err == nil {
				entries = append(entries, sub...)
			}
		}
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

	// Derive workspace from the encoded project path.
	workspace := workspaceFromPath(path)

	// Generate a stable ID from source path.
	id := sessionID(path)

	// Extract inter-session communication links and stats.
	links := ExtractLinks(entries)
	stats := ExtractStats(entries)

	// Extract team links (native Gemini CLI agent teams).
	teamLinks := ExtractTeamLinks(entries)
	links = append(links, teamLinks...)

	// Build metadata.
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

	// Try to extract the iTerm2 session ID from tool results.
	itermSID := extractItermSessionID(entries)
	if itermSID != "" {
		meta["iterm_session"] = itermSID
	}

	// Classify team role from JSONL data.
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
		Metadata:   meta,
		TeamName:   teamName,
		AgentName:  agentName,
		IsTeamLead: isTeamLead,
	}, nil
}

func (c *GeminiCLI) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	gh, err := cc.GeminiHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(gh, "projects"), nil
}
