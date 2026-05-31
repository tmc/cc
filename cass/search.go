package cass

import "time"

// SearchMode controls the type of search.
type SearchMode int

const (
	// SearchLexical performs FTS5 full-text search.
	SearchLexical SearchMode = iota
	// SearchSemantic is reserved for future embedding search.
	SearchSemantic
	// SearchHybrid is reserved for combined lexical and semantic search.
	SearchHybrid
)

// SortMode controls result ordering.
type SortMode string

const (
	// SortRecent orders results by ended_at descending (most recently active).
	SortRecent SortMode = "recent"
	// SortRelevance orders results by BM25 rank; only meaningful with a query.
	SortRelevance SortMode = "relevance"
	// SortStarted orders results by started_at descending.
	SortStarted SortMode = "started"
	// SortOldest orders results by started_at ascending.
	SortOldest SortMode = "oldest"
)

// ChildMode controls how workflow child-agent matches are presented in search
// results.
type ChildMode string

const (
	// ChildrenCollapsed folds child workflow-agent matches into the parent
	// session row. It is the default.
	ChildrenCollapsed ChildMode = "collapsed"
	// ChildrenExpanded folds matches but marks them for inline child display.
	ChildrenExpanded ChildMode = "expanded"
	// ChildrenRaw is reserved for surfacing child sessions as top-level rows.
	ChildrenRaw ChildMode = "raw"
)

// SearchRequest encapsulates query parameters.
type SearchRequest struct {
	Query    string
	Mode     SearchMode
	Sort     SortMode
	Filters  Filters
	Children ChildMode // child-agent display mode; empty means collapsed.
	Limit    int
	Offset   int

	// SummaryOnly omits nested detail payloads from hits. Use for search result
	// lists that fetch full session metadata only after a row is opened.
	SummaryOnly bool

	// SkipCount avoids the exact total-count query. SearchResult.TotalCount is
	// then the visible lower bound unless TotalCountExact is true.
	SkipCount bool
}

// Filters constrains search results.
type Filters struct {
	Agent        string    // Filter by agent slug.
	Workspace    string    // Filter by workspace path.
	GitCommonDir string    // Filter by resolved git common dir (stable across worktrees).
	Team         string    // Filter by agent team name.
	GoalStatus   string    // Filter by goal status.
	Skill        string    // Filter by used skill name or path substring.
	After        time.Time // Sessions started after this time.
	Before       time.Time // Sessions started before this time.
}

// SearchResult holds search results.
type SearchResult struct {
	Hits            []Hit `json:"hits"`
	TotalCount      int   `json:"total_count"`
	TotalCountExact bool  `json:"total_count_exact"`
}

// Hit is a single search result.
type Hit struct {
	SessionID    string  `json:"session_id"`
	Agent        string  `json:"agent"`
	Title        string  `json:"title"`
	Snippet      string  `json:"snippet"`
	Score        float64 `json:"score"`
	Workspace    string  `json:"workspace,omitempty"`
	GitCommonDir string  `json:"git_common_dir,omitempty"`
	Branch       string  `json:"branch,omitempty"`
	SourcePath   string  `json:"source_path,omitempty"`
	StartedAt    string  `json:"started_at,omitempty"`
	EndedAt      string  `json:"ended_at,omitempty"`

	// Stats summary (populated when available).
	ToolCalls                int    `json:"tool_calls,omitempty"`
	Turns                    int    `json:"turns,omitempty"`
	InputTokens              int    `json:"input_tokens,omitempty"`
	OutputTokens             int    `json:"output_tokens,omitempty"`
	CacheReads               int    `json:"cache_reads,omitempty"`
	CacheCreationInputTokens int    `json:"cache_creation_input_tokens,omitempty"`
	FilesEdited              int    `json:"files_edited,omitempty"`
	LinesWritten             int    `json:"lines_written,omitempty"`
	DurationSecs             int    `json:"duration_secs,omitempty"`
	Sparkline                string `json:"sparkline,omitempty"`
	Compactions              int    `json:"compactions,omitempty"`
	SubagentSpawns           int    `json:"subagent_spawns,omitempty"`
	SubagentEntries          int    `json:"subagent_entries,omitempty"`
	SubagentMirroredEntries  int    `json:"subagent_mirrored_entries,omitempty"`
	AgentProgressEvents      int    `json:"agent_progress_events,omitempty"`
	AgentProgressMirrors     int    `json:"agent_progress_mirrors,omitempty"`
	AgentProgressUnmatched   int    `json:"agent_progress_unmatched,omitempty"`
	IT2Sends                 int    `json:"it2_sends,omitempty"`
	IT2Screens               int    `json:"it2_screens,omitempty"`
	IT2Splits                int    `json:"it2_splits,omitempty"`
	APIRequestCount          int    `json:"api_request_count,omitempty"`

	ToolBreakdown map[string]int `json:"tool_breakdown,omitempty"`

	// Agent teams context.
	TeamName   string `json:"team_name,omitempty"`
	AgentName  string `json:"agent_name,omitempty"`
	IsTeamLead bool   `json:"is_team_lead,omitempty"`

	// Goal-mode context.
	Goals              []Goal `json:"goals,omitempty"`
	GoalCount          int    `json:"goal_count,omitempty"`
	ActiveGoalCount    int    `json:"active_goal_count,omitempty"`
	CompletedGoalCount int    `json:"completed_goal_count,omitempty"`

	// Skill context.
	Skills             []SkillUse `json:"skills,omitempty"`
	SkillCount         int        `json:"skill_count,omitempty"`
	SelectedSkillCount int        `json:"selected_skill_count,omitempty"`
	LoadedSkillCount   int        `json:"loaded_skill_count,omitempty"`

	// Native workflow context.
	Workflows           []WorkflowRun `json:"workflows,omitempty"`
	WorkflowCount       int           `json:"workflow_count,omitempty"`
	WorkflowAgentCount  int           `json:"workflow_agent_count,omitempty"`
	WorkflowTaskOpCount int           `json:"workflow_task_op_count,omitempty"`

	// Folded child-workflow matches. When a query matches one or more workflow
	// runs (or, in future, their child agents) the match is bubbled to this
	// parent row rather than shown as separate child rows.
	WorkflowMatchCount      int      `json:"workflow_match_count,omitempty"`
	MatchedWorkflowIDs      []string `json:"matched_workflow_ids,omitempty"`
	MatchedWorkflowNames    []string `json:"matched_workflow_names,omitempty"`
	MatchedWorkflowAgentIDs []string `json:"matched_workflow_agent_ids,omitempty"`
	CollapsedChildren       bool     `json:"collapsed_children,omitempty"`

	// SummaryOnly marks web search list rows whose nested detail payload was
	// intentionally omitted. Fetch /api/session/{id}/meta for full detail.
	SummaryOnly bool `json:"summary_only,omitempty"`
}
