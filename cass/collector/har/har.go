package har

import (
	"encoding/json"
)

// File represents a Proxyman HAR export file (single request/response pair).
type File struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is the HTTP request portion of a HAR entry.
type Request struct {
	Method      string       `json:"method"`
	URL         string       `json:"url"`
	HTTPVersion string       `json:"httpVersion"`
	Headers     []Header     `json:"headers"`
	PostData    *PostData    `json:"postData,omitempty"`
	HeadersSize int          `json:"headersSize"`
	BodySize    int          `json:"bodySize"`
}

// Response is the HTTP response portion of a HAR entry.
type Response struct {
	Status     int      `json:"status"`
	StatusText string   `json:"statusText"`
	Headers    []Header `json:"headers"`
	Content    Content  `json:"content"`
}

// Header is a name-value pair from HTTP headers.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// PostData holds the request body.
type PostData struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"` // JSON string of the Messages API request body.
}

// Content holds the response body, typically base64-encoded SSE.
type Content struct {
	Size     int    `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`     // Base64-encoded SSE stream (or raw text).
	Encoding string `json:"encoding"` // "base64" for Proxyman exports.
}

// messagesAPIRequest is the subset of an Anthropic Messages API request body
// needed for context breakdown analysis.
type messagesAPIRequest struct {
	Model    string            `json:"model"`
	System   json.RawMessage   `json:"system"`
	Tools    json.RawMessage   `json:"tools"`
	Messages json.RawMessage   `json:"messages"`
	Metadata *requestMetadata  `json:"metadata,omitempty"`
}

// requestMetadata extracts the user_id field which contains the session ID.
type requestMetadata struct {
	UserID string `json:"user_id"`
}

// sseMessageStart is the message_start SSE event data.
type sseMessageStart struct {
	Type    string `json:"type"`
	Message struct {
		Model string   `json:"model"`
		ID    string   `json:"id"`
		Usage sseUsage `json:"usage"`
	} `json:"message"`
}

// sseMessageDelta is the message_delta SSE event (contains final usage).
type sseMessageDelta struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage sseUsage `json:"usage"`
}

// sseUsage holds token counts from SSE events.
type sseUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}
