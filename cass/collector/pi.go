package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/ccpaths"
)

// Pi collects sessions from pi (pi-coding-agent) JSONL files.
//
// pi stores one session per file under
// <agent-dir>/sessions/<encoded-project>/<timestamp>_<uuid>.jsonl, where the
// agent dir is ~/.pi/agent by default or PI_CODING_AGENT_DIR if set. The files
// are normalized to the canonical cc.Entry shape by cc.ReadFile, so this
// collector is thin: it discovers the files and assembles a cass.Session from
// the shared summary/stats/links helpers.
type Pi struct {
	// Root overrides the default <PiHome>/sessions directory.
	Root string
}

// Name returns the agent slug "pi".
func (c *Pi) Name() string { return "pi" }

// Detect reports whether pi session data is present on the system.
func (c *Pi) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks pi session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *Pi) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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
		sess, err := c.parseSession(ctx, config, root)
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

func (c *Pi) scanDir(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
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

		sess, err := c.parseSession(ctx, config, path)
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

func (c *Pi) parseSession(ctx context.Context, config cass.ScanConfig, path string) (cass.Session, error) {
	entries, err := readSessionFile(ctx, config, path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	sum := cc.Summarize(path, entries)

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

	stats := ExtractStats(entries)
	links := ExtractLinks(entries)
	skills := ExtractSkills(entries, c.Name())

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

	id := sum.SessionID
	if id == "" {
		id = sessionID(path)
	}

	return cass.Session{
		ID:         id,
		Agent:      c.Name(),
		Title:      titleFromSummary(sum),
		Workspace:  sum.CWD,
		SourcePath: path,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Skills:     skills,
		Stats:      stats,
		Metadata:   meta,
	}, nil
}

func (c *Pi) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := ccpaths.PiHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "sessions"), nil
}
