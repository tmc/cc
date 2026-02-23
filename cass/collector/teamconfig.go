package collector

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass/store"
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

// ScanTeamConfigs reads all ~/.claude/teams/*/config.json and ~/.gemini/teams/*/config.json files and returns
// parsed TeamConfig records. Pass an empty root to use the default location.
func ScanTeamConfigs(root string) ([]store.TeamConfig, error) {
	var roots []string
	if root == "" {
		ch, err := cc.ClaudeHome()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		roots = append(roots, filepath.Join(ch, "teams"))

		gh, err := cc.GeminiHome()
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

	// Re-encode members array as compact JSON for storage.
	membersJSON := "[]"
	if len(raw.Members) > 0 {
		if b, err := json.Marshal(raw.Members); err == nil {
			membersJSON = string(b)
		}
	}

	return store.TeamConfig{
		Name:          raw.Name,
		LeadSessionID: raw.LeadSessionID,
		LeadAgentID:   raw.LeadAgentID,
		Description:   raw.Description,
		CreatedAt:     raw.CreatedAt / 1000, // ms → unix seconds
		MembersJSON:   membersJSON,
	}, nil
}
