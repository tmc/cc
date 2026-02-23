package collector

import (
	"context"
	"os"
	"path/filepath"

	"github.com/tmc/cc/cass"
)

// Antigravity collects sessions from Google Antigravity.
type Antigravity struct {
	// Root overrides the default ~/.gemini/antigravity directory.
	Root string
}

func (c *Antigravity) Name() string { return "antigravity" }

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

	// TODO: implement scanning of .gemini/antigravity/brain directories
	// For now, this is a clean placeholder skeleton.

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
