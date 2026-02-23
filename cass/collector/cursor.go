package collector

import (
	"context"
	"os"
	"path/filepath"

	"github.com/tmc/cc/cass"
)

// Cursor collects sessions from Cursor IDE workspace storage.
type Cursor struct {
	// Root overrides the default workspaceStorage directory.
	Root string
}

func (c *Cursor) Name() string { return "cursor" }

func (c *Cursor) Detect(ctx context.Context) (*cass.DetectionResult, error) {
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

func (c *Cursor) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		root, err := c.root()
		if err != nil {
			return err
		}
		paths = []string{root}
	}

	// TODO: implement scanning of Cursor workspace storage / SQLite databases.
	// For now, this is a clean placeholder skeleton.

	return nil
}

func (c *Cursor) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	// Default Cursor path on macOS (similar paths exist for Linux/Windows)
	return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "workspaceStorage"), nil
}
