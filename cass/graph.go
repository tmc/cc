package cass

// SessionLink represents an interaction between two sessions.
// Links are categorized into kinds:
//   - Messages: send-text, send-key (active communication from source to target)
//   - Observations: get-screen, get-buffer (source reading target's state)
//   - Team: team-spawn, team-message (native Claude Code agent teams)
type SessionLink struct {
	SourceSession string `json:"source_session"` // iTerm2 session ID or agent name.
	TargetSession string `json:"target_session"` // iTerm2 session ID or agent name.
	Kind          string `json:"kind"`           // "message", "observation", or "team".
	Action        string `json:"action"`         // "send-text", "team-spawn", "team-message", etc.
	Text          string `json:"text,omitempty"` // Content excerpt.
	Timestamp     string `json:"timestamp,omitempty"`
	TeamName      string `json:"team_name,omitempty"` // Team name for team links.
}

// GraphData holds combined node and link data for the session graph.
type GraphData struct {
	Nodes     []GraphNode   `json:"nodes"`
	Links     []SessionLink `json:"links"`
	TimeRange TimeRange     `json:"time_range"`
}

// GraphNode represents a session in the communication graph.
type GraphNode struct {
	ID           string `json:"id"` // iTerm2 session ID (short prefix).
	Workspace    string `json:"workspace"`
	GitCommonDir string `json:"git_common_dir,omitempty"`
	Title        string `json:"title"`
	StartedAt    int64  `json:"started_at,omitempty"`
	ToolCalls    int    `json:"tool_calls,omitempty"`
	Turns        int    `json:"turns,omitempty"`
	Tokens       int    `json:"tokens,omitempty"` // input + output.
	IsActive     bool   `json:"is_active"`
	TeamName     string `json:"team_name,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
}

// TimeRange is the min/max timestamp range for graph data.
type TimeRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}
