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

// Codex collects sessions from Codex JSONL files.
type Codex struct {
	// Root overrides the default ~/.codex/sessions directory.
	Root string
}

// Name returns the agent slug "codex".
func (c *Codex) Name() string { return "codex" }

// Detect reports whether Codex session data is present on the system.
func (c *Codex) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks Codex session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *Codex) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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
		if !strings.HasSuffix(root, ".jsonl") {
			continue
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			continue
		}
		sess, err := c.parseSession(root)
		if err != nil {
			continue
		}
		if config.Project != "" && !matchProject(sess, config.Project) {
			continue
		}
		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Codex) scanDir(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}

		sess, err := c.parseSession(path)
		if err != nil {
			return nil
		}
		if config.Project != "" && !matchProject(sess, config.Project) {
			return nil
		}

		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

func (c *Codex) parseSession(path string) (cass.Session, error) {
	entries, err := cc.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	sum := cc.Summarize(path, entries)
	agent := codexAgent(entries)

	var messages []cass.Message
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := strings.TrimSpace(e.Message.TextContent())
		if text == "" {
			continue
		}
		messages = append(messages, cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		})
	}

	links := ExtractLinks(entries)
	stats := ExtractStats(entries)

	meta := map[string]any{}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}
	if originator, source := codexOriginSource(entries); originator != "" || source != "" {
		if originator != "" {
			meta["originator"] = originator
		}
		if source != "" {
			meta["source"] = source
		}
	}

	id := sum.SessionID
	if id == "" {
		id = sessionID(path)
	}

	return cass.Session{
		ID:         id,
		Agent:      agent,
		Title:      titleFromSummary(sum),
		Workspace:  sum.CWD,
		SourcePath: path,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Stats:      stats,
		Metadata:   meta,
	}, nil
}

func (c *Codex) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	ch, err := cc.CodexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(ch, "sessions"), nil
}

func codexAgent(entries []cc.Entry) string {
	for _, e := range entries {
		switch {
		case e.Originator == "codex_cli_rs" || e.Source == "cli":
			return "codex-cli"
		case e.Originator == "Codex Desktop" || e.Source == "vscode":
			return "codex-app"
		}
	}
	return "codex-cli"
}

func codexOriginSource(entries []cc.Entry) (originator, source string) {
	for _, e := range entries {
		if originator == "" && e.Originator != "" {
			originator = e.Originator
		}
		if source == "" && e.Source != "" {
			source = e.Source
		}
		if originator != "" && source != "" {
			return originator, source
		}
	}
	return originator, source
}
