package cc

import (
	"encoding/json"
	"strings"
)

// ContentBlocks parses the message content as an array of ContentBlock.
// Returns nil if content is a plain string or unparseable.
func (m *Message) ContentBlocks() []ContentBlock {
	var blocks []ContentBlock
	if json.Unmarshal(m.Content, &blocks) == nil {
		return blocks
	}
	return nil
}

// TextContent returns the concatenated text from the message content.
func (m *Message) TextContent() string {
	return ExtractAnyText(m.Content)
}

// ToolUses returns only the tool_use blocks from message content.
func (m *Message) ToolUses() []ContentBlock {
	var uses []ContentBlock
	for _, b := range m.ContentBlocks() {
		if b.Type == "tool_use" {
			uses = append(uses, b)
		}
	}
	return uses
}

// ToolResults returns only the tool_result blocks from message content.
func (m *Message) ToolResults() []ContentBlock {
	var results []ContentBlock
	for _, b := range m.ContentBlocks() {
		if b.Type == "tool_result" {
			results = append(results, b)
		}
	}
	return results
}

// IsToolResultOnly reports whether all parsed content blocks are tool results.
func (m *Message) IsToolResultOnly() bool {
	blocks := m.ContentBlocks()
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// BashCommand returns the command field from a Bash tool_use block's Input.
// Returns "" if the block is not a Bash tool_use or the input is unparseable.
func (b ContentBlock) BashCommand() string {
	if b.Type != "tool_use" || b.Name != "Bash" || len(b.Input) == 0 {
		return ""
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(b.Input, &in) != nil {
		return ""
	}
	return in.Command
}

// ImageBlocks returns image-bearing blocks from the message content.
func (m *Message) ImageBlocks() []ContentBlock {
	var images []ContentBlock
	for _, b := range m.ContentBlocks() {
		switch b.Type {
		case "image", "input_image", "local_image":
			images = append(images, b)
		}
	}
	return images
}

// ExtractAnyText returns text from string, block-array, or nested JSON values.
func ExtractAnyText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var anyVal any
	if json.Unmarshal(raw, &anyVal) != nil {
		return ""
	}
	var parts []string
	collectText(&parts, anyVal)
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func collectText(parts *[]string, v any) {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) != "" {
			*parts = append(*parts, x)
		}
	case []any:
		for _, it := range x {
			collectText(parts, it)
		}
	case map[string]any:
		if t, ok := x["text"].(string); ok && strings.TrimSpace(t) != "" {
			*parts = append(*parts, t)
			return
		}
		// Prefer common content/parts keys first, then fallback to all values.
		foundPreferred := false
		if c, ok := x["content"]; ok {
			collectText(parts, c)
			foundPreferred = true
		}
		if p, ok := x["parts"]; ok {
			collectText(parts, p)
			foundPreferred = true
		}
		if foundPreferred {
			return
		}
		for _, vv := range x {
			collectText(parts, vv)
		}
	}
}
