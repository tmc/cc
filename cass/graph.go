package cass

import "slices"

// Node types for the workflow-aware graph. They populate GraphNode.NodeType and
// the node_type query filter on /api/graph.
const (
	NodeTypeSession       = "session"        // a normal top-level coding-agent session.
	NodeTypeWorkflow      = "workflow"       // one native Claude Code workflow run.
	NodeTypeWorkflowAgent = "workflow_agent" // a child agent within a workflow run.
	NodeTypeTeamAgent     = "team_agent"     // a team member node from inter-session links.
	NodeTypeSubagent      = "subagent"       // a subagent run spawned by a session (Task or spawn_agent).
)

// Edge types for graph links. They populate SessionLink.EdgeType. The legacy
// Kind/Action fields are preserved; EdgeType is the normalized, explicit kind.
const (
	EdgeWorkflowContains = "workflow_contains" // session -> workflow, or workflow -> child agent.
	EdgeWorkflowSpawn    = "workflow_spawn"    // workflow -> child agent fan-out.
	EdgeSubagentSpawn    = "subagent_spawn"    // session -> subagent run it spawned.
	EdgeTeamMessage      = "team_message"
	EdgeTeamSpawn        = "team_spawn"
	EdgeItermMessage     = "iterm_message"
	EdgeItermObserve     = "iterm_observe"
	EdgeItermSplit       = "iterm_split"
)

// WorkflowMode selects how /api/graph treats native workflow runs.
type WorkflowMode string

const (
	// WorkflowCollapsed includes workflow parent nodes with aggregate child
	// stats but not the individual child-agent nodes. It is the default.
	WorkflowCollapsed WorkflowMode = "collapsed"
	// WorkflowExpanded additionally includes child workflow-agent nodes.
	WorkflowExpanded WorkflowMode = "expanded"
	// WorkflowNone produces the legacy link-centric graph with no workflow
	// nodes, for debugging.
	WorkflowNone WorkflowMode = "none"
)

// GraphOptions controls graph construction. The zero value selects the legacy
// link-centric graph (WorkflowNone) with no node-type filter.
type GraphOptions struct {
	Workflow  WorkflowMode // collapsed, expanded, or none.
	NodeTypes []string     // optional allow-list of node_type values; empty = all.
}

// IncludeNode reports whether a node of the given type passes the NodeTypes filter.
func (o GraphOptions) IncludeNode(nodeType string) bool {
	if len(o.NodeTypes) == 0 {
		return true
	}
	return slices.Contains(o.NodeTypes, nodeType)
}

// SessionLink represents an interaction between two sessions.
// Links are categorized into kinds:
//   - Messages: send-text, send-key (active communication from source to target)
//   - Observations: get-screen, get-buffer (source reading target's state)
//   - Team: team-spawn, team-message (native Claude Code agent teams)
type SessionLink struct {
	SourceSession string `json:"source_session"`      // iTerm2 session ID or agent name.
	TargetSession string `json:"target_session"`      // iTerm2 session ID or agent name.
	Kind          string `json:"kind"`                // "message", "observation", or "team".
	Action        string `json:"action"`              // "send-text", "team-spawn", "team-message", etc.
	EdgeType      string `json:"edge_type,omitempty"` // normalized edge kind; see Edge* constants.
	Text          string `json:"text,omitempty"`      // Content excerpt.
	Timestamp     string `json:"timestamp,omitempty"`
	TeamName      string `json:"team_name,omitempty"` // Team name for team links.
}

// GraphData holds combined node and link data for the session graph.
type GraphData struct {
	Nodes     []GraphNode   `json:"nodes"`
	Links     []SessionLink `json:"links"`
	TimeRange TimeRange     `json:"time_range"`
}

// GraphNode represents a node in the graph: a session, a workflow run, a
// workflow child agent, or a team agent. The NodeType field discriminates;
// fields not relevant to a given type are omitted.
type GraphNode struct {
	ID           string `json:"id"` // session id, workflow run id, agent id, or iTerm2 prefix.
	NodeType     string `json:"node_type,omitempty"`
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

	// Workflow / workflow-agent fields (populated by node type).
	ParentSessionID    string   `json:"parent_session_id,omitempty"`
	WorkflowRunID      string   `json:"workflow_run_id,omitempty"`
	Name               string   `json:"name,omitempty"`
	TaskID             string   `json:"task_id,omitempty"`
	WorkflowAgentIndex int      `json:"workflow_agent_index,omitempty"`
	Label              string   `json:"label,omitempty"`
	Phase              string   `json:"phase,omitempty"`
	AgentType          string   `json:"agent_type,omitempty"`
	Description        string   `json:"description,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	ScriptPath         string   `json:"script_path,omitempty"`
	TranscriptDir      string   `json:"transcript_dir,omitempty"`
	SourcePath         string   `json:"source_path,omitempty"`
	Status             string   `json:"status,omitempty"`
	AgentCount         int      `json:"agent_count,omitempty"`
	JournalEventCount  int      `json:"journal_event_count,omitempty"`
	CompletedAt        int64    `json:"completed_at,omitempty"`
	WorkflowCount      int      `json:"workflow_count,omitempty"` // session nodes: number of workflow runs.
	WorkflowNames      []string `json:"workflow_names,omitempty"`
}

// TimeRange is the min/max timestamp range for graph data.
type TimeRange struct {
	Min string `json:"min"`
	Max string `json:"max"`
}
