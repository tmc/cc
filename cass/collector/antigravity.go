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
)

// Antigravity collects sessions from Google Antigravity.
type Antigravity struct {
	// Root overrides the default ~/.gemini/antigravity directory.
	Root string
}

// Name returns the agent slug "antigravity".
func (c *Antigravity) Name() string { return "antigravity" }

// Detect reports whether Antigravity session data is present on the system.
func (c *Antigravity) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks Antigravity brain artifact directories and sends decoded sessions
// to out. It closes out when scanning completes.
func (c *Antigravity) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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
		if err := c.scanPath(ctx, root, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Antigravity) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini", "antigravity"), nil
}

func (c *Antigravity) scanPath(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	info, err := os.Stat(root)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		return nil
	}

	if isAntigravityBrainSessionDir(root) {
		return c.emitSession(ctx, root, config, out)
	}

	brain := root
	if filepath.Base(root) != "brain" {
		brain = filepath.Join(root, "brain")
	}
	if info, err := os.Stat(brain); err != nil || !info.IsDir() {
		return nil
	}
	return c.scanBrainRoot(ctx, brain, config, out)
}

func (c *Antigravity) scanBrainRoot(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read antigravity brain dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if !isAntigravityBrainSessionDir(dir) {
			continue
		}
		if err := c.emitSession(ctx, dir, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *Antigravity) emitSession(ctx context.Context, dir string, config cass.ScanConfig, out chan<- cass.Session) error {
	session, err := c.parseBrainSession(dir)
	if err != nil {
		return nil
	}
	if !config.Since.IsZero() && session.EndedAt.Before(config.Since) {
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
}

func (c *Antigravity) parseBrainSession(dir string) (cass.Session, error) {
	entries, err := cc.ReadFile(context.Background(), dir)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", dir)
	}

	sum := cc.Summarize(dir, entries)

	var messages []cass.Message
	var turns int
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := strings.TrimSpace(e.Message.TextContent())
		if text == "" {
			continue
		}
		// Calculate turns like claudecode/pi collectors
		if e.Message.Role == "user" && !e.Message.IsToolResultOnly() {
			turns++
		}
		messages = append(messages, cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		})
	}

	stats := ExtractStats(entries)
	// Override turns with our computed count to keep consistency
	stats.Turns = turns

	links := ExtractLinks(entries)
	skills := ExtractSkills(entries, c.Name())

	meta := map[string]any{
		"brain_id": filepath.Base(dir),
	}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}

	meta["artifact_count"] = len(messages)

	// Preserve legacy artifacts list for compatibility with existing DB queries
	var artifacts []map[string]string
	for _, e := range entries {
		if strings.Contains(e.UUID, ":") {
			parts := strings.Split(e.UUID, ":")
			fileName := parts[len(parts)-1]
			artifacts = append(artifacts, map[string]string{
				"file": fileName,
			})
		}
	}
	meta["artifacts"] = artifacts

	id := sum.SessionID
	if id == "" {
		id = "ag-" + filepath.Base(dir)
	}

	durationSecs := stats.DurationSecs
	if durationSecs == 0 && !sum.FirstTime.IsZero() && !sum.LastTime.IsZero() {
		durationSecs = int(sum.LastTime.Sub(sum.FirstTime).Seconds())
	}
	stats.DurationSecs = durationSecs

	return cass.Session{
		ID:         id,
		Agent:      c.Name(),
		Title:      antigravityTitle(entries, dir),
		Workspace:  sum.CWD,
		SourcePath: dir,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Skills:     skills,
		Stats:      stats,
		Metadata:   meta,
	}, nil
}

func antigravityTitle(entries []cc.Entry, dir string) string {
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "user" {
			continue
		}
		text := e.Message.TextContent()
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			line = strings.TrimLeft(line, "#")
			line = strings.TrimSpace(line)
			line = strings.TrimPrefix(line, "- [ ]")
			line = strings.TrimPrefix(line, "- [x]")
			line = strings.TrimPrefix(line, "- [X]")
			line = strings.TrimSpace(line)
			if line != "" {
				if len(line) > 80 {
					return line[:80] + "..."
				}
				return line
			}
		}
	}
	return filepath.Base(dir)
}

func isAntigravityBrainSessionDir(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".system_generated", "logs", "transcript.jsonl")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "task.md")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "agent_prompt.md")); err == nil {
		return true
	}
	return false
}
