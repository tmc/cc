package har

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/tmc/cc/cass"
)

var harSkillNameRe = regexp.MustCompile(`(?m)^- ([A-Za-z0-9_.-]+(?::[A-Za-z0-9_.-]+)?): .*\(file: [^)]+/SKILL\.md\)|^## Skill:\s*([A-Za-z0-9_.:-]+)|<skill(?:\s+name=["']?([A-Za-z0-9_.:-]+)["']?)?`)

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
	skillNames := map[string]bool{}

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
			skillNames[entry.Name] = true
		default:
			bd.BuiltinToolBytes += bytes
		}
	}
	appendSkillNames(bd, skillNames)
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
	skillNames := map[string]bool{}

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
		if kind == "skill" || strings.Contains(block.Text, "### Available skills") {
			for _, name := range skillNamesFromText(block.Text) {
				skillNames[name] = true
			}
		}
	}
	appendSkillNames(bd, skillNames)
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

func skillNamesFromText(text string) []string {
	var names []string
	for _, m := range harSkillNameRe.FindAllStringSubmatch(text, -1) {
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				names = append(names, m[i])
				break
			}
		}
	}
	return names
}

func appendSkillNames(bd *cass.ContextBreakdown, names map[string]bool) {
	if len(names) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, name := range bd.SkillNames {
		seen[name] = true
	}
	for name := range names {
		if !seen[name] {
			bd.SkillNames = append(bd.SkillNames, name)
		}
	}
	sort.Strings(bd.SkillNames)
}
