package collector

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/tmc/cc/cass"
)

// Cursor collects sessions from Cursor IDE workspace storage.
type Cursor struct {
	// Root overrides the default workspaceStorage directory.
	Root string
}

// Name returns the agent slug "cursor".
func (c *Cursor) Name() string { return "cursor" }

// Detect reports whether Cursor workspace data is present on the system.
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

// Scan walks Cursor session paths and sends decoded sessions to out.
// It closes out when scanning completes. Scanning is currently a placeholder
// and produces no sessions.
func (c *Cursor) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	return errors.New("cursor scan not implemented")
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
