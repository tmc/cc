package cc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// TeamConfig represents a team's configuration stored at
// ~/.claude/teams/{teamName}/config.json.
type TeamConfig struct {
	Name          string       `json:"name"`
	Description   string       `json:"description,omitempty"`
	CreatedAt     int64        `json:"createdAt"`
	LeadAgentID   string       `json:"leadAgentId"`
	LeadSessionID string       `json:"leadSessionId"`
	Members       []TeamMember `json:"members"`
}

// TeamMember is an agent registered in a team.
type TeamMember struct {
	AgentID       string   `json:"agentId"`
	Name          string   `json:"name"`
	AgentType     string   `json:"agentType"`
	Model         string   `json:"model,omitempty"`
	Color         string   `json:"color,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	BackendType   string   `json:"backendType,omitempty"`
	IsActive      bool     `json:"isActive,omitempty"`
	JoinedAt      int64    `json:"joinedAt"`
	TmuxPaneID    string   `json:"tmuxPaneId,omitempty"`
	CWD           string   `json:"cwd"`
	Subscriptions []string `json:"subscriptions,omitempty"`
}

// TeamsDir returns the path to the teams directory.
// If CC_TEAMS_DIR is set, it is used; otherwise defaults to ~/.claude/teams.
func TeamsDir() (string, error) {
	if dir := os.Getenv("CC_TEAMS_DIR"); dir != "" {
		return dir, nil
	}
	ch, err := ClaudeHome()
	if err != nil {
		return "", fmt.Errorf("teams dir: %w", err)
	}
	return filepath.Join(ch, "teams"), nil
}

// TeamDir returns the path to a specific team's directory.
func TeamDir(teamName string) (string, error) {
	base, err := TeamsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, teamName), nil
}

// TeamConfigPath returns the path to a team's config.json.
func TeamConfigPath(teamName string) (string, error) {
	dir, err := TeamDir(teamName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ReadTeamConfig reads a team's configuration from disk.
func ReadTeamConfig(teamName string) (*TeamConfig, error) {
	path, err := TeamConfigPath(teamName)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read team config %q: %w", teamName, err)
	}
	var cfg TeamConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse team config %q: %w", teamName, err)
	}
	return &cfg, nil
}

// WriteTeamConfig writes a team's configuration to disk, creating directories
// as needed.
func WriteTeamConfig(teamName string, cfg *TeamConfig) error {
	dir, err := TeamDir(teamName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create team dir %q: %w", teamName, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal team config: %w", err)
	}
	path := filepath.Join(dir, "config.json")
	return os.WriteFile(path, data, 0o644)
}

// ListTeams returns the names of all teams.
func ListTeams() ([]string, error) {
	dir, err := TeamsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list teams: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only include directories that have a config.json.
		cfg := filepath.Join(dir, e.Name(), "config.json")
		if _, err := os.Stat(cfg); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// DeleteTeam removes a team's directory and all associated data.
func DeleteTeam(teamName string) error {
	dir, err := TeamDir(teamName)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// AddTeamMember adds or updates a member in the team config.
func AddTeamMember(teamName string, member TeamMember) error {
	cfg, err := ReadTeamConfig(teamName)
	if err != nil {
		return err
	}
	// Update existing member or append.
	found := false
	for i, m := range cfg.Members {
		if m.AgentID == member.AgentID {
			cfg.Members[i] = member
			found = true
			break
		}
	}
	if !found {
		cfg.Members = append(cfg.Members, member)
	}
	return WriteTeamConfig(teamName, cfg)
}

// RemoveTeamMember removes a member from the team config by agent ID.
func RemoveTeamMember(teamName, agentID string) error {
	cfg, err := ReadTeamConfig(teamName)
	if err != nil {
		return err
	}
	members := cfg.Members[:0]
	for _, m := range cfg.Members {
		if m.AgentID != agentID {
			members = append(members, m)
		}
	}
	cfg.Members = members
	return WriteTeamConfig(teamName, cfg)
}
