package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"

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

// Scan walks Antigravity session paths and sends decoded sessions to out.
// It closes out when scanning completes. Scanning is currently a placeholder
// and produces no sessions.
func (c *Antigravity) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	return errors.New("antigravity scan not implemented")
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
