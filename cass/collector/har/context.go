package har

import (
	"encoding/json"
	"strings"

	"github.com/tmc/cc/cass"
)

// ParseContextBreakdown extracts per-tool and per-system-block attribution
// from a raw Messages API request body JSON string.
//
// It extends the coarse byte counts already in cass.APIRequest with named
// breakdowns: which tool costs how many bytes, which MCP server is heaviest,
// and what each system block contains.
func ParseContextBreakdown(body string) cass.ContextBreakdown {
	var bd cass.ContextBreakdown
	bd.TotalRequestBytes = len(body)

	var apiReq messagesAPIRequest
	if err := json.Unmarshal([]byte(body), &apiReq); err != nil {
		return bd
	}

	bd.SystemPromptBytes = len(apiReq.System)
	bd.ToolDefinitionBytes = len(apiReq.Tools)
	bd.ConversationBytes = len(apiReq.Messages)

	parseToolBreakdown(&bd, apiReq.Tools)
	parseSystemBreakdown(&bd, apiReq.System)

	// Message count.
	var msgs []json.RawMessage
	if json.Unmarshal(apiReq.Messages, &msgs) == nil {
		bd.MessageCount = len(msgs)
	}

	return bd
}

// parseToolBreakdown attributes bytes to each named tool and aggregates by category.
func parseToolBreakdown(bd *cass.ContextBreakdown, raw json.RawMessage) {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return
	}
	bd.ToolCount = len(tools)
	if len(tools) == 0 {
		return
	}

	bd.ToolBytes = make(map[string]int, len(tools))
	bd.MCPServerBytes = make(map[string]int)

	for _, t := range tools {
		var entry cass.ToolEntry
		if json.Unmarshal(t, &entry) != nil || entry.Name == "" {
			continue
		}
		bytes := len(t)
		bd.ToolBytes[entry.Name] = bytes

		switch {
		case strings.HasPrefix(entry.Name, "mcp__"):
			bd.MCPToolBytes += bytes
			server := mcpServerName(entry.Name)
			bd.MCPServerBytes[server] += bytes
		case isSkillTool(entry.Name):
			bd.SkillToolBytes += bytes
		default:
			bd.BuiltinToolBytes += bytes
		}
	}
}

// parseSystemBreakdown classifies each system block by content kind.
func parseSystemBreakdown(bd *cass.ContextBreakdown, raw json.RawMessage) {
	// System can be a string (simple) or an array of typed blocks.
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		// Scalar string — treat as a single text block.
		bd.SystemBlockCount = 1
		bd.SystemBlockBytes = map[string]int{"text": len(raw)}
		return
	}

	bd.SystemBlockCount = len(blocks)
	if len(blocks) == 0 {
		return
	}

	bd.SystemBlockBytes = make(map[string]int, 4)

	for _, b := range blocks {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		}
		if json.Unmarshal(b, &block) != nil {
			bd.SystemBlockBytes["unknown"] += len(b)
			continue
		}

		kind := classifySystemBlock(block.Type, block.Text)
		bd.SystemBlockBytes[kind] += len(b)
	}
}

// classifySystemBlock determines the semantic kind of a system block.
// Kinds: "claude_md", "skill", "tool_result", "text", "unknown".
func classifySystemBlock(blockType, text string) string {
	switch blockType {
	case "tool_result":
		return "tool_result"
	case "text", "":
		return classifyTextBlock(text)
	default:
		return blockType
	}
}

// classifyTextBlock heuristically identifies text block content.
func classifyTextBlock(text string) string {
	// CLAUDE.md injections contain project instructions markers.
	if strings.Contains(text, "# Claude Code") ||
		strings.Contains(text, "CLAUDE.md") ||
		strings.Contains(text, "## Agent Guidelines") ||
		strings.Contains(text, "project instructions") {
		return "claude_md"
	}
	// Skill expansions are large structured text blocks prefixed with skill headers.
	if strings.Contains(text, "<skill") || strings.Contains(text, "## Skill:") {
		return "skill"
	}
	return "text"
}

// mcpServerName extracts the server slug from an mcp__<server>__<tool> name.
func mcpServerName(toolName string) string {
	// Format: mcp__<server>__<tool>
	parts := strings.SplitN(toolName, "__", 3)
	if len(parts) >= 2 {
		return parts[1]
	}
	return "unknown"
}

// isSkillTool returns true for tool names that look like invoked skills.
// Skills show up as tool definitions injected by the Skill tool invocation.
// Heuristic: not a known builtin, not mcp__, but matches skill naming patterns.
func isSkillTool(name string) bool {
	// Skills are invoked via the Skill tool and don't follow mcp__ prefix convention.
	// They tend to be kebab-case or have recognizable skill-like names.
	// For now: anything not builtin and not mcp__ is treated as a skill or unknown tool.
	return !cass.BuiltinTools[name] && !strings.HasPrefix(name, "mcp__")
}
