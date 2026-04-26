package cc

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// TaskNotification is the parsed content of a queue-operation enqueue
// whose content is a <task-notification> XML blob, posted by Claude Code
// when a Task subagent completes.
//
// The fields mirror the on-disk shape:
//
//	<task-notification>
//	  <task-id>...</task-id>
//	  <tool-use-id>...</tool-use-id>
//	  <output-file>...</output-file>
//	  <status>completed|error|...</status>
//	  <summary>...</summary>
//	  <result>...</result>
//	  <usage>
//	    <total_tokens>N</total_tokens>
//	    <tool_uses>N</tool_uses>
//	    <duration_ms>N</duration_ms>
//	  </usage>
//	  <worktree>
//	    <worktreePath>...</worktreePath>
//	    <worktreeBranch>...</worktreeBranch>
//	  </worktree>
//	</task-notification>
//
// TaskID equals the agentId in the subagent JSONL filename
// (agent-<agentId>.jsonl), which is the authoritative join from a parent
// session's queue-operation events to its subagent runs.
type TaskNotification struct {
	TaskID         string
	ToolUseID      string
	OutputFile     string
	Status         string
	Summary        string
	Result         string
	TotalTokens    int
	ToolUses       int
	DurationMs     int64
	WorktreePath   string
	WorktreeBranch string
}

// taskNotificationXML is the wire shape used only for unmarshalling.
type taskNotificationXML struct {
	XMLName    xml.Name `xml:"task-notification"`
	TaskID     string   `xml:"task-id"`
	ToolUseID  string   `xml:"tool-use-id"`
	OutputFile string   `xml:"output-file"`
	Status     string   `xml:"status"`
	Summary    string   `xml:"summary"`
	Result     string   `xml:"result"`
	Usage      struct {
		TotalTokens int   `xml:"total_tokens"`
		ToolUses    int   `xml:"tool_uses"`
		DurationMs  int64 `xml:"duration_ms"`
	} `xml:"usage"`
	Worktree struct {
		WorktreePath   string `xml:"worktreePath"`
		WorktreeBranch string `xml:"worktreeBranch"`
	} `xml:"worktree"`
}

// ParseTaskNotification parses a queue-operation content string. It returns
// ok=false (and no error) when the content does not look like a
// <task-notification> blob, which is the common case for human-input
// enqueues. A malformed task-notification returns ok=false as well; callers
// that want to distinguish should use ParseTaskNotificationStrict.
func ParseTaskNotification(content string) (TaskNotification, bool) {
	tn, err := ParseTaskNotificationStrict(content)
	if err != nil {
		return TaskNotification{}, false
	}
	return tn, true
}

// ParseTaskNotificationStrict parses a queue-operation content string and
// returns an error when the content is recognizably a <task-notification>
// but malformed. Plain user text (no opening tag) returns a sentinel
// "not a task notification" error; callers may treat that as benign.
func ParseTaskNotificationStrict(content string) (TaskNotification, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "<task-notification") {
		return TaskNotification{}, errNotTaskNotification
	}
	var x taskNotificationXML
	if err := xml.Unmarshal([]byte(trimmed), &x); err != nil {
		return TaskNotification{}, fmt.Errorf("task-notification: %w", err)
	}
	return TaskNotification{
		TaskID:         strings.TrimSpace(x.TaskID),
		ToolUseID:      strings.TrimSpace(x.ToolUseID),
		OutputFile:     strings.TrimSpace(x.OutputFile),
		Status:         strings.TrimSpace(x.Status),
		Summary:        x.Summary,
		Result:         x.Result,
		TotalTokens:    x.Usage.TotalTokens,
		ToolUses:       x.Usage.ToolUses,
		DurationMs:     x.Usage.DurationMs,
		WorktreePath:   strings.TrimSpace(x.Worktree.WorktreePath),
		WorktreeBranch: strings.TrimSpace(x.Worktree.WorktreeBranch),
	}, nil
}

// errNotTaskNotification is a sentinel returned when content has no
// <task-notification opening tag. Not exported because callers should use
// the boolean form (ParseTaskNotification) for the common case.
var errNotTaskNotification = fmt.Errorf("not a task notification")

// SubagentMeta is the parsed content of agent-<agentId>.meta.json,
// the sidecar Claude Code writes next to each subagent JSONL.
type SubagentMeta struct {
	AgentType   string `json:"agentType,omitempty"`
	Description string `json:"description,omitempty"`
}

// ReadSubagentMeta reads a subagent meta.json sidecar. When the file does
// not exist it returns a zero SubagentMeta and a nil error: callers should
// treat absence as normal, not as an error.
func ReadSubagentMeta(path string) (SubagentMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SubagentMeta{}, nil
		}
		return SubagentMeta{}, fmt.Errorf("read subagent meta %q: %w", path, err)
	}
	var m SubagentMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return SubagentMeta{}, fmt.Errorf("parse subagent meta %q: %w", path, err)
	}
	return m, nil
}
