package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccpaths"
)

// teamConfigJSON matches the shape of ~/.claude/teams/<name>/config.json.
type teamConfigJSON struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	CreatedAt     int64             `json:"createdAt"`
	LeadAgentID   string            `json:"leadAgentId"`
	LeadSessionID string            `json:"leadSessionId"`
	Members       []json.RawMessage `json:"members"`
}

type teamMemberJSON struct {
	AgentID       string   `json:"agentId"`
	Name          string   `json:"name"`
	AgentType     string   `json:"agentType"`
	Model         string   `json:"model"`
	Color         string   `json:"color"`
	Prompt        string   `json:"prompt"`
	BackendType   string   `json:"backendType"`
	IsActive      bool     `json:"isActive"`
	JoinedAt      int64    `json:"joinedAt"`
	TmuxPaneID    string   `json:"tmuxPaneId"`
	CWD           string   `json:"cwd"`
	Subscriptions []string `json:"subscriptions"`
}

// ScanTeamConfigs reads all ~/.claude/teams/*/config.json and ~/.gemini/teams/*/config.json files and returns
// parsed TeamConfig records. Pass an empty root to use the default location.
func ScanTeamConfigs(root string) ([]store.TeamConfig, error) {
	var roots []string
	if root == "" {
		ch, err := ccpaths.ClaudeHome()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		roots = append(roots, filepath.Join(ch, "teams"))

		gh, err := ccpaths.GeminiHome()
		if err == nil && gh != "" {
			roots = append(roots, filepath.Join(gh, "teams"))
		}
	} else {
		roots = []string{root}
	}

	var configs []store.TeamConfig

	for _, r := range roots {
		entries, err := os.ReadDir(r)
		if err != nil {
			if os.IsNotExist(err) {
				continue // teams dir doesn't exist — not an error
			}
			return nil, fmt.Errorf("read teams dir %s: %w", r, err)
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			cfgPath := filepath.Join(r, e.Name(), "config.json")
			tc, err := parseTeamConfig(cfgPath)
			if err != nil {
				continue // skip unreadable configs
			}
			configs = append(configs, tc)
		}
	}
	return configs, nil
}

func parseTeamConfig(path string) (store.TeamConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return store.TeamConfig{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw teamConfigJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return store.TeamConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}

	// Re-encode members array as compact JSON for storage and also normalize
	// each known field into queryable team_members rows.
	membersJSON := "[]"
	if len(raw.Members) > 0 {
		if b, err := json.Marshal(raw.Members); err == nil {
			membersJSON = string(b)
		}
	}
	members := make([]store.TeamMember, 0, len(raw.Members))
	for i, rawMember := range raw.Members {
		var m teamMemberJSON
		if err := json.Unmarshal(rawMember, &m); err != nil {
			continue
		}
		subscriptions := "[]"
		if len(m.Subscriptions) > 0 {
			if b, err := json.Marshal(m.Subscriptions); err == nil {
				subscriptions = string(b)
			}
		}
		members = append(members, store.TeamMember{
			TeamName:          raw.Name,
			Ordinal:           i,
			AgentID:           m.AgentID,
			Name:              m.Name,
			AgentType:         m.AgentType,
			Model:             m.Model,
			Color:             m.Color,
			Prompt:            m.Prompt,
			BackendType:       m.BackendType,
			IsActive:          m.IsActive,
			JoinedAt:          m.JoinedAt / 1000,
			TmuxPaneID:        m.TmuxPaneID,
			CWD:               m.CWD,
			SubscriptionsJSON: subscriptions,
		})
	}

	return store.TeamConfig{
		Name:          raw.Name,
		LeadSessionID: raw.LeadSessionID,
		LeadAgentID:   raw.LeadAgentID,
		Description:   raw.Description,
		CreatedAt:     raw.CreatedAt / 1000, // ms → unix seconds
		MembersJSON:   membersJSON,
		Members:       members,
	}, nil
}
