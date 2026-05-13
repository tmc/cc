package cc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// AgentDef is a user-defined agent template stored at
// ~/.claude/agents/<name>.json (or under ~/.claude/agents/.disabled/).
// It declares triggers, capabilities, and the tool surface an agent is
// expected to use. Distinct from in-session subagent runs (see Subagent).
type AgentDef struct {
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Version       string         `json:"version,omitempty"`
	Triggers      AgentTriggers  `json:"triggers,omitempty"`
	Capabilities  []string       `json:"capabilities,omitempty"`
	Tools         []string       `json:"tools,omitempty"`
	OutputFormats []string       `json:"outputFormats,omitempty"`
	ExampleUsage  []string       `json:"example_usage,omitempty"`
	Workflow      []string       `json:"workflow,omitempty"`
	Command       string         `json:"command,omitempty"`
	Flags         map[string]any `json:"flags,omitempty"`

	// Disabled is true when the definition is under the .disabled/ subtree.
	Disabled bool `json:"-"`
	// SourcePath is the absolute path the definition was read from.
	SourcePath string `json:"-"`
}

// AgentTriggers groups optional keyword and regex triggers.
type AgentTriggers struct {
	Keywords []string `json:"keywords,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

// AgentsDir returns the directory holding user agent definitions.
// If CC_AGENTS_DIR is set it is used; otherwise ~/.claude/agents.
func AgentsDir() (string, error) {
	if dir := os.Getenv("CC_AGENTS_DIR"); dir != "" {
		return dir, nil
	}
	ch, err := ClaudeHome()
	if err != nil {
		return "", fmt.Errorf("agents dir: %w", err)
	}
	return filepath.Join(ch, "agents"), nil
}

// ReadAgentDef parses a single agent definition JSON file.
func ReadAgentDef(path string) (*AgentDef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent def: %w", err)
	}
	var a AgentDef
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("parse agent def %s: %w", path, err)
	}
	a.SourcePath = path
	a.Disabled = strings.Contains(path, string(os.PathSeparator)+".disabled"+string(os.PathSeparator))
	if a.Name == "" {
		a.Name = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	return &a, nil
}

// ListAgentDefs walks ~/.claude/agents (including .disabled/) and returns
// all definitions sorted by name. Unreadable files are skipped silently.
func ListAgentDefs() ([]*AgentDef, error) {
	root, err := AgentsDir()
	if err != nil {
		return nil, err
	}
	return ListAgentDefsIn(root)
}

// ListAgentDefsIn walks the given directory tree for agent definitions.
func ListAgentDefsIn(root string) ([]*AgentDef, error) {
	var defs []*AgentDef
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		a, err := ReadAgentDef(path)
		if err != nil {
			return nil
		}
		defs = append(defs, a)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("walk agents dir: %w", err)
	}
	sort.Slice(defs, func(i, k int) bool { return defs[i].Name < defs[k].Name })
	return defs, nil
}
