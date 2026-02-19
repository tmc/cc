package cc

import "encoding/json"

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
	// Try as string first.
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	// Try as blocks.
	var out string
	for _, b := range m.ContentBlocks() {
		if b.Type == "text" && b.Text != "" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
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
