package har

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

// userIDPattern extracts all three components from the metadata.user_id field.
// Format: "user_<hash>_account_<uuid>_session_<uuid>"
// Group 1: user hash, Group 2: account UUID, Group 3: session UUID.
var userIDPattern = regexp.MustCompile(`user_([^_]+(?:_[^_]+)*)_account_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})_session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// sessionIDPattern extracts only the session UUID (fallback for older formats).
var sessionIDPattern = regexp.MustCompile(`session_([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// ParseFile reads a Proxyman HAR export file and returns an APIRequest.
func ParseFile(path string) (*cass.APIRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return parseEntry(&f, path)
}

// parseEntry converts a HAR file entry into a cass.APIRequest.
func parseEntry(f *File, sourcePath string) (*cass.APIRequest, error) {
	// Only process Anthropic API calls.
	if !strings.Contains(f.Request.URL, "anthropic.com") {
		return nil, fmt.Errorf("not an anthropic request: %s", f.Request.URL)
	}

	req := &cass.APIRequest{
		SourceFile: sourcePath,
		StatusCode: f.Response.Status,
	}

	// Source hash for deduplication.
	requestID := headerValue(f.Response.Headers, "request-id")
	req.RequestID = requestID
	req.SourceHash = computeSourceHash(requestID, sourcePath)

	// Timestamp from response Date header.
	if dateStr := headerValue(f.Response.Headers, "Date"); dateStr != "" {
		if t, err := time.Parse(time.RFC1123, dateStr); err == nil {
			req.Timestamp = t.Unix()
		}
	}

	// ID is derived from request-id and timestamp.
	req.ID = computeID(requestID, req.Timestamp)

	// Parse request body for model, session linkage, and context breakdown.
	if f.Request.PostData != nil && f.Request.PostData.Text != "" {
		parseRequestBody(f.Request.PostData.Text, req)
	}

	// Parse response body (base64-decoded SSE) for final token usage.
	if f.Response.Content.Text != "" {
		parseResponseBody(&f.Response.Content, req)
	}

	// Extract rate-limit headers and organization ID.
	req.RateLimits = extractRateLimits(f.Response.Headers, req.Timestamp)
	req.OrgID = headerValue(f.Response.Headers, "x-organization-id")

	// Extract duration from x-envoy-upstream-service-time header.
	if dur := headerValue(f.Response.Headers, "x-envoy-upstream-service-time"); dur != "" {
		if ms, err := strconv.Atoi(dur); err == nil {
			req.DurationMs = ms
		}
	}

	return req, nil
}

// parseRequestBody extracts model, session ID, and context breakdown from the
// Messages API request JSON.
func parseRequestBody(body string, req *cass.APIRequest) {
	req.TotalRequestBytes = len(body)

	var apiReq messagesAPIRequest
	if err := json.Unmarshal([]byte(body), &apiReq); err != nil {
		return
	}

	// Model.
	req.Model = apiReq.Model
	req.ModelFamily = normalizeModelFamily(apiReq.Model)

	// Purpose classification.
	req.Purpose = classifyPurpose(&apiReq)

	// Context breakdown (coarse byte counts + per-tool/block attribution).
	bd := ParseContextBreakdown(body)
	req.SystemPromptBytes = bd.SystemPromptBytes
	req.ToolDefinitionBytes = bd.ToolDefinitionBytes
	req.ConversationBytes = bd.ConversationBytes
	req.Breakdown = &bd

	// Identity fields from metadata.user_id.
	// Format: "user_<hash>_account_<uuid>_session_<uuid>"
	if apiReq.Metadata != nil {
		parseUserID(apiReq.Metadata.UserID, req)
	}
}

// parseResponseBody decodes the base64 SSE stream and extracts final token usage.
func parseResponseBody(content *Content, req *cass.APIRequest) {
	var sseData []byte
	if content.Encoding == "base64" {
		var err error
		sseData, err = base64.StdEncoding.DecodeString(content.Text)
		if err != nil {
			return
		}
	} else {
		sseData = []byte(content.Text)
	}

	// Parse SSE events for message_delta (final usage) and message_start.
	scanner := bufio.NewScanner(bytes.NewReader(sseData))
	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		switch currentEvent {
		case "message_delta":
			var delta sseMessageDelta
			if json.Unmarshal([]byte(data), &delta) == nil {
				// message_delta contains the final, authoritative token counts.
				req.InputTokens = delta.Usage.InputTokens
				req.OutputTokens = delta.Usage.OutputTokens
				req.CacheReadTokens = delta.Usage.CacheReadInputTokens
				req.CacheCreationTokens = delta.Usage.CacheCreationInputTokens
				req.StopReason = delta.Delta.StopReason
			}
		case "message_start":
			// Fallback: if no message_delta, use message_start usage.
			if req.InputTokens == 0 {
				var start sseMessageStart
				if json.Unmarshal([]byte(data), &start) == nil {
					req.InputTokens = start.Message.Usage.InputTokens
					req.CacheReadTokens = start.Message.Usage.CacheReadInputTokens
					req.CacheCreationTokens = start.Message.Usage.CacheCreationInputTokens
				}
			}
		}
	}
}

// extractRateLimits parses anthropic-ratelimit-unified-* response headers.
func extractRateLimits(headers []Header, timestamp int64) cass.RateLimitSnapshot {
	snap := cass.RateLimitSnapshot{Timestamp: timestamp}

	for _, h := range headers {
		name := strings.ToLower(h.Name)
		switch name {
		case "anthropic-ratelimit-unified-5h-utilization":
			snap.Utilization5h, _ = strconv.ParseFloat(h.Value, 64)
		case "anthropic-ratelimit-unified-5h-reset":
			snap.Reset5h, _ = strconv.ParseInt(h.Value, 10, 64)
		case "anthropic-ratelimit-unified-7d-utilization":
			snap.Utilization7d, _ = strconv.ParseFloat(h.Value, 64)
		case "anthropic-ratelimit-unified-7d-reset":
			snap.Reset7d, _ = strconv.ParseInt(h.Value, 10, 64)
		case "anthropic-ratelimit-unified-representative-claim":
			snap.RepresentativeClaim = h.Value
		default:
			// Per-model sub-buckets: anthropic-ratelimit-unified-7d_sonnet-utilization
			if strings.HasPrefix(name, "anthropic-ratelimit-unified-7d_") {
				suffix := strings.TrimPrefix(name, "anthropic-ratelimit-unified-")
				parts := strings.SplitN(suffix, "-", 2)
				if len(parts) == 2 {
					bucket := parts[0] // e.g. "7d_sonnet"
					field := parts[1]  // e.g. "utilization", "reset", "status"
					snap.ModelBucket = bucket
					switch field {
					case "utilization":
						snap.ModelUtilization, _ = strconv.ParseFloat(h.Value, 64)
					case "reset":
						snap.ModelReset, _ = strconv.ParseInt(h.Value, 10, 64)
					}
				}
			}
		}
	}

	return snap
}

// parseUserID extracts user_hash, account_uuid, and session_uuid from the
// metadata.user_id field. Format: "user_<hash>_account_<uuid>_session_<uuid>".
// Falls back to session-only extraction for older/partial formats.
func parseUserID(userID string, req *cass.APIRequest) {
	if m := userIDPattern.FindStringSubmatch(userID); len(m) == 4 {
		req.UserHash = m[1]
		req.AccountUUID = m[2]
		req.SessionID = m[3]
		return
	}
	// Fallback: extract session UUID only.
	req.SessionID = extractSessionID(userID)
}

// extractSessionID parses the Claude session UUID from a metadata.user_id string.
// Format: "user_..._account_..._session_<UUID>"
func extractSessionID(userID string) string {
	m := sessionIDPattern.FindStringSubmatch(userID)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// normalizeModelFamily returns a short family name from a full model ID.
func normalizeModelFamily(model string) string {
	lower := strings.ToLower(model)
	switch {
	case strings.Contains(lower, "opus"):
		return "opus"
	case strings.Contains(lower, "sonnet"):
		return "sonnet"
	case strings.Contains(lower, "haiku"):
		return "haiku"
	default:
		return "unknown"
	}
}

// classifyPurpose determines the purpose of an API call from its request body.
func classifyPurpose(req *messagesAPIRequest) string {
	// Haiku calls with structured output for topic classification are classifiers.
	if strings.Contains(req.Model, "haiku") {
		// Check if tools array is empty (classifiers don't use tools).
		var tools []json.RawMessage
		if json.Unmarshal(req.Tools, &tools) == nil && len(tools) == 0 {
			return "classifier"
		}
	}

	// Check for compaction (system entries with summary content).
	var system []json.RawMessage
	if json.Unmarshal(req.System, &system) == nil {
		for _, s := range system {
			var block struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(s, &block) == nil {
				if strings.Contains(block.Text, "compact_boundary") || strings.Contains(block.Text, "compacted summary") {
					return "compact"
				}
			}
		}
	}

	return "response"
}

// headerValue returns the first matching header value (case-insensitive name).
func headerValue(headers []Header, name string) string {
	lower := strings.ToLower(name)
	for _, h := range headers {
		if strings.ToLower(h.Name) == lower {
			return h.Value
		}
	}
	return ""
}

func computeID(requestID string, timestamp int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", requestID, timestamp)))
	return fmt.Sprintf("%x", h[:16])
}

func computeSourceHash(requestID, sourcePath string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", requestID, filepath.Base(sourcePath))))
	return fmt.Sprintf("%x", h[:16])
}

// ScanDir walks a directory for Proxyman HAR export files and returns parsed
// API requests. Non-Anthropic requests and unparseable files are silently skipped.
func ScanDir(dir string) ([]cass.APIRequest, error) {
	return ScanDirContext(context.Background(), dir)
}

// ScanDirContext is like ScanDir but stops early when ctx is canceled.
func ScanDirContext(ctx context.Context, dir string) ([]cass.APIRequest, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var results []cass.APIRequest
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		req, err := ParseFile(path)
		if err != nil {
			continue // skip non-Anthropic or unparseable files
		}
		results = append(results, *req)
	}

	return results, nil
}
