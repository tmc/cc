package ccdistill

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Format names a supported SFT output encoding.
type Format string

const (
	FormatAnthropic Format = "anthropic" // messages[] with tool_use/tool_result blocks verbatim
	FormatOpenAI    Format = "openai"    // {messages:[{role,content}]}, tool calls flattened to text
	FormatShareGPT  Format = "sharegpt"  // {conversations:[{from,value}]}
)

// Encoder serializes one [Sample] to a format's record shape.
type Encoder interface {
	// Encode writes one JSON record for s (no trailing newline) to w.
	Encode(w io.Writer, s *Sample) error
}

// NewEncoder returns the [Encoder] for f. An unknown Format is an error.
func NewEncoder(f Format) (Encoder, error) {
	switch f {
	case FormatAnthropic:
		return anthropicEncoder{}, nil
	case FormatOpenAI:
		return openaiEncoder{}, nil
	case FormatShareGPT:
		return shareGPTEncoder{}, nil
	default:
		return nil, fmt.Errorf("unknown format: %q", string(f))
	}
}

// writeJSON marshals v compactly and writes it without a trailing newline.
func writeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("write record: %w", err)
	}
	return nil
}

// anthropicEncoder emits the native Anthropic messages shape: a top-level
// "system" string (from a leading system turn, if any) and a "messages" array
// whose user and assistant turns carry tool_use and tool_result blocks
// verbatim.
type anthropicEncoder struct{}

type anthropicRecord struct {
	System   string             `json:"system,omitempty"`
	Messages []anthropicMessage `json:"messages"`
	Meta     Meta               `json:"meta"`
}

type anthropicMessage struct {
	Role    string        `json:"role"`
	Content []anthropicCB `json:"content"`
}

// anthropicCB is a content block in the Anthropic shape; only the fields
// relevant to its type are set.
type anthropicCB struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

func (anthropicEncoder) Encode(w io.Writer, s *Sample) error {
	rec := anthropicRecord{Meta: s.Meta}
	for _, t := range s.Turns {
		if t.Role == RoleSystem {
			rec.System = joinText(t.Blocks)
			continue
		}
		msg := anthropicMessage{Role: t.Role, Content: []anthropicCB{}}
		for _, b := range t.Blocks {
			switch b.Kind {
			case KindText, KindThinking:
				msg.Content = append(msg.Content, anthropicCB{Type: "text", Text: b.Text})
			case KindToolUse:
				if b.ToolUse == nil {
					continue
				}
				msg.Content = append(msg.Content, anthropicCB{
					Type:  "tool_use",
					ID:    b.ToolUse.ID,
					Name:  b.ToolUse.Name,
					Input: b.ToolUse.Input,
				})
			case KindToolResult:
				if b.ToolRes == nil {
					continue
				}
				msg.Content = append(msg.Content, anthropicCB{
					Type:      "tool_result",
					ToolUseID: b.ToolRes.ToolUseID,
					Content:   b.ToolRes.Content,
					IsError:   b.ToolRes.IsError,
				})
			}
		}
		rec.Messages = append(rec.Messages, msg)
	}
	return writeJSON(w, rec)
}

// openaiEncoder emits {messages:[{role,content}]} with tool calls and results
// flattened into the message text, which trainers expecting a flat chat shape
// can consume directly.
type openaiEncoder struct{}

type openaiRecord struct {
	Messages []openaiMessage `json:"messages"`
	Meta     Meta            `json:"meta"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (openaiEncoder) Encode(w io.Writer, s *Sample) error {
	rec := openaiRecord{Meta: s.Meta}
	for _, t := range s.Turns {
		rec.Messages = append(rec.Messages, openaiMessage{
			Role:    t.Role,
			Content: renderText(t.Blocks),
		})
	}
	return writeJSON(w, rec)
}

// shareGPTEncoder emits {conversations:[{from,value}]} using the ShareGPT role
// names (system, human, gpt), with tool calls and results flattened to text.
type shareGPTEncoder struct{}

type shareGPTRecord struct {
	Conversations []shareGPTTurn `json:"conversations"`
	Meta          Meta           `json:"meta"`
}

type shareGPTTurn struct {
	From  string `json:"from"`
	Value string `json:"value"`
}

func (shareGPTEncoder) Encode(w io.Writer, s *Sample) error {
	rec := shareGPTRecord{Meta: s.Meta}
	for _, t := range s.Turns {
		rec.Conversations = append(rec.Conversations, shareGPTTurn{
			From:  shareGPTFrom(t.Role),
			Value: renderText(t.Blocks),
		})
	}
	return writeJSON(w, rec)
}

func shareGPTFrom(role string) string {
	switch role {
	case RoleSystem:
		return "system"
	case RoleUser:
		return "human"
	case RoleAssistant:
		return "gpt"
	default:
		return role
	}
}

// joinText concatenates the text of text and thinking blocks with newlines.
func joinText(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		if (b.Kind == KindText || b.Kind == KindThinking) && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// renderText flattens a turn's blocks to a single string for the openai and
// sharegpt encoders: text verbatim, tool calls and results as labeled lines.
func renderText(blocks []Block) string {
	var parts []string
	for _, b := range blocks {
		switch b.Kind {
		case KindText, KindThinking:
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case KindToolUse:
			if b.ToolUse != nil {
				parts = append(parts, fmt.Sprintf("[tool_use %s] %s", b.ToolUse.Name, string(b.ToolUse.Input)))
			}
		case KindToolResult:
			if b.ToolRes != nil {
				label := "tool_result"
				if b.ToolRes.IsError {
					label = "tool_result error"
				}
				parts = append(parts, fmt.Sprintf("[%s] %s", label, b.ToolRes.Content))
			}
		}
	}
	return strings.Join(parts, "\n")
}
