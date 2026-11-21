package collector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

// OpenClaw collects sessions from OpenClaw's JSONL session files.
// Sessions live at ~/.openclaw/agents/<agent-id>/sessions/<session-id>.jsonl.
type OpenClaw struct {
	// Root overrides the default ~/.openclaw/agents directory.
	Root string
}

// Name returns the agent slug "openclaw".
func (c *OpenClaw) Name() string { return "openclaw" }

// Detect reports whether OpenClaw session data is present on the system.
func (c *OpenClaw) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

// Scan walks OpenClaw session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *OpenClaw) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
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

// scanPath handles an agents root, an agent's sessions directory, or one session file.
func (c *OpenClaw) scanPath(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("stat openclaw path: %w", err)
	}
	if !info.IsDir() {
		return c.scanSessionFile(ctx, root, info, config, out)
	}
	if filepath.Base(root) == "sessions" {
		return c.scanSessionsDir(ctx, root, config, out)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read agents dir: %w", err)
	}
	for _, agent := range entries {
		if !agent.IsDir() {
			continue
		}
		if err := c.scanSessionsDir(ctx, filepath.Join(root, agent.Name(), "sessions"), config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *OpenClaw) scanSessionsDir(ctx context.Context, dir string, config cass.ScanConfig, out chan<- cass.Session) error {
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read openclaw sessions: %w", err)
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		if err := c.scanSessionFile(ctx, filepath.Join(dir, f.Name()), info, config, out); err != nil {
			return err
		}
	}
	return nil
}

func (c *OpenClaw) scanSessionFile(ctx context.Context, path string, info os.FileInfo, config cass.ScanConfig, out chan<- cass.Session) error {
	if !strings.HasSuffix(path, ".jsonl") || (!config.Since.IsZero() && info.ModTime().Before(config.Since)) {
		return nil
	}
	agent := filepath.Base(filepath.Dir(filepath.Dir(path)))
	session, err := c.parseSession(path, agent)
	if err != nil || (config.Project != "" && !matchProject(session, config.Project)) {
		return nil
	}
	select {
	case out <- session:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type openclawEvent struct {
	Type      string       `json:"type"`
	ID        string       `json:"id"`
	Timestamp string       `json:"timestamp"`
	CWD       string       `json:"cwd,omitempty"`
	Provider  string       `json:"provider,omitempty"`
	ModelID   string       `json:"modelId,omitempty"`
	Message   *openclawMsg `json:"message,omitempty"`
}

type openclawMsg struct {
	Role    string          `json:"role"`
	Content []openclawBlock `json:"content"`
}

type openclawBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (c *OpenClaw) parseSession(path, agentID string) (cass.Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}

	sess := cass.Session{
		Agent:      c.Name(),
		SourcePath: path,
		Metadata:   map[string]any{"agent_id": agentID},
	}

	// Derive a stable ID from the file path.
	h := sha256.Sum256([]byte(path))
	sess.ID = fmt.Sprintf("oc-%x", h[:8])

	var model string
	var lastTime time.Time

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev openclawEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}

		t, _ := time.Parse(time.RFC3339Nano, ev.Timestamp)
		if !t.IsZero() {
			if sess.StartedAt.IsZero() {
				sess.StartedAt = t
			}
			lastTime = t
		}

		switch ev.Type {
		case "session":
			if ev.ID != "" {
				// Use the canonical session ID from the header event.
				sess.ID = ev.ID
			}
			if ev.CWD != "" {
				sess.Workspace = ev.CWD
			}
		case "model_change":
			if ev.ModelID != "" {
				model = ev.ModelID
				sess.Metadata["model"] = model
			}
		case "message":
			if ev.Message == nil {
				continue
			}
			var text strings.Builder
			for _, block := range ev.Message.Content {
				if block.Type == "text" {
					text.WriteString(block.Text)
				}
			}
			if text.Len() == 0 {
				continue
			}
			sess.Messages = append(sess.Messages, cass.Message{
				ID:        ev.ID,
				Role:      ev.Message.Role,
				Content:   text.String(),
				CreatedAt: t,
			})
		}
	}

	if !lastTime.IsZero() {
		sess.EndedAt = lastTime
	}

	// Build a title from the first user message (truncated).
	for _, m := range sess.Messages {
		if m.Role == "user" {
			title := strings.TrimSpace(m.Content)
			// Strip system prefix lines (e.g. "System: [...]" or metadata blocks).
			for _, line := range strings.Split(title, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "System:") || strings.HasPrefix(line, "```") {
					continue
				}
				if len(line) > 80 {
					line = line[:77] + "..."
				}
				sess.Title = line
				break
			}
			if sess.Title != "" {
				break
			}
		}
	}
	if sess.Title == "" {
		sess.Title = filepath.Base(path)
	}

	// Count user turns for stats.
	for _, m := range sess.Messages {
		if m.Role == "user" {
			sess.Stats.Turns++
		}
	}

	return sess, nil
}

func (c *OpenClaw) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".openclaw", "agents"), nil
}
