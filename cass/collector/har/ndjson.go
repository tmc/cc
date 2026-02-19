package har

import (
	"bufio"
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

// artifactPathPattern matches ~/.it2/sessions/<uuid>/proxy-traffic.<pid>.jsonl
// and extracts the session UUID and PID.
var artifactPathPattern = regexp.MustCompile(
	`/sessions/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})/proxy-traffic\.(\d+)\.jsonl$`,
)

// proxyEntry is the proxyman.Entry NDJSON format written by proxyman-export.
// Each line of a proxy-traffic.<pid>.jsonl file is one of these.
type proxyEntry struct {
	ID              string              `json:"id,omitempty"`
	Timestamp       time.Time           `json:"timestamp"`
	Method          string              `json:"method"`
	URL             string              `json:"url"`
	Domain          string              `json:"domain"`
	StatusCode      int                 `json:"status_code"`
	RequestHeaders  map[string][]string `json:"request_headers,omitempty"`
	ResponseHeaders map[string][]string `json:"response_headers,omitempty"`
	RequestBody     string              `json:"request_body,omitempty"`
	ResponseBody    string              `json:"response_body,omitempty"`
	DurationMs      int64               `json:"duration_ms"`
	ClientPid       int                 `json:"client_pid,omitempty"`
}

// ParseArtifactFile reads a proxy-traffic.<pid>.jsonl file and returns
// parsed APIRequests. The it2SessionID and clientPID are extracted from
// the file path; only Anthropic Messages API entries are returned.
func ParseArtifactFile(path string) ([]cass.APIRequest, error) {
	it2SessionID, clientPID := extractArtifactPathInfo(path)

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var results []cass.APIRequest
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e proxyEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		req := convertProxyEntry(&e, path, it2SessionID, clientPID)
		if req == nil {
			continue
		}
		results = append(results, *req)
	}
	return results, scanner.Err()
}

// ScanArtifactDirs globs ~/.it2/sessions/*/proxy-traffic.*.jsonl and parses
// each file. Non-Anthropic entries and parse errors are silently skipped.
// The default root is ~/.it2/sessions; pass an override for testing.
func ScanArtifactDirs(root string) ([]cass.APIRequest, error) {
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		root = filepath.Join(home, ".it2", "sessions")
	}

	pattern := filepath.Join(root, "*", "proxy-traffic.*.jsonl")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}

	var all []cass.APIRequest
	for _, path := range paths {
		reqs, err := ParseArtifactFile(path)
		if err != nil {
			continue // skip unreadable files
		}
		all = append(all, reqs...)
	}
	return all, nil
}

// convertProxyEntry converts a proxyEntry to a cass.APIRequest.
// Returns nil if the entry is not an Anthropic Messages API call.
func convertProxyEntry(e *proxyEntry, sourcePath, it2SessionID string, clientPID int) *cass.APIRequest {
	if !strings.Contains(e.URL, "anthropic.com") {
		return nil
	}
	// Only index Messages API calls (not token-count, not hello).
	if !strings.Contains(e.URL, "/v1/messages") || strings.Contains(e.URL, "count_tokens") {
		return nil
	}

	// Use clientPID from path if the entry doesn't have one.
	pid := e.ClientPid
	if pid == 0 {
		pid = clientPID
	}

	req := &cass.APIRequest{
		SourceFile:   sourcePath,
		StatusCode:   e.StatusCode,
		DurationMs:   int(e.DurationMs),
		IT2SessionID: it2SessionID,
		ClientPID:    pid,
	}

	// Timestamp: zero time means the exporter didn't capture it yet — skip.
	if !e.Timestamp.IsZero() && e.Timestamp.Year() > 2000 {
		req.Timestamp = e.Timestamp.Unix()
	}

	// Request-id for deduplication (from response headers).
	requestID := headerValueMap(e.ResponseHeaders, "request-id")
	req.RequestID = requestID
	req.SourceHash = computeSourceHash(requestID, sourcePath)
	req.ID = computeID(requestID, req.Timestamp)

	// Parse request body.
	if e.RequestBody != "" {
		parseRequestBody(e.RequestBody, req)
	}

	// Parse response body (raw SSE — not base64 encoded in NDJSON format).
	if e.ResponseBody != "" {
		content := &Content{
			Text:     e.ResponseBody,
			Encoding: "", // raw, not base64
		}
		parseResponseBody(content, req)
	}

	// Rate-limit headers.
	req.RateLimits = extractRateLimitsMap(e.ResponseHeaders, req.Timestamp)

	return req
}

// extractArtifactPathInfo parses the it2 session UUID and PID from a
// proxy-traffic file path. Returns empty strings / 0 if not matched.
func extractArtifactPathInfo(path string) (it2SessionID string, clientPID int) {
	m := artifactPathPattern.FindStringSubmatch(path)
	if len(m) < 3 {
		return "", 0
	}
	pid, _ := strconv.Atoi(m[2])
	return m[1], pid
}

// headerValueMap returns the first value for a header name (case-insensitive)
// from a map[string][]string (proxyman format, unlike HAR's []Header).
func headerValueMap(headers map[string][]string, name string) string {
	lower := strings.ToLower(name)
	for k, vs := range headers {
		if strings.ToLower(k) == lower && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// extractRateLimitsMap parses rate-limit headers from a map[string][]string.
func extractRateLimitsMap(headers map[string][]string, timestamp int64) cass.RateLimitSnapshot {
	snap := cass.RateLimitSnapshot{Timestamp: timestamp}
	for k, vs := range headers {
		if len(vs) == 0 {
			continue
		}
		name := strings.ToLower(k)
		val := vs[0]
		switch name {
		case "anthropic-ratelimit-unified-5h-utilization":
			snap.Utilization5h, _ = strconv.ParseFloat(val, 64)
		case "anthropic-ratelimit-unified-5h-reset":
			snap.Reset5h, _ = strconv.ParseInt(val, 10, 64)
		case "anthropic-ratelimit-unified-7d-utilization":
			snap.Utilization7d, _ = strconv.ParseFloat(val, 64)
		case "anthropic-ratelimit-unified-7d-reset":
			snap.Reset7d, _ = strconv.ParseInt(val, 10, 64)
		case "anthropic-ratelimit-unified-representative-claim":
			snap.RepresentativeClaim = val
		default:
			if strings.HasPrefix(name, "anthropic-ratelimit-unified-7d_") {
				suffix := strings.TrimPrefix(name, "anthropic-ratelimit-unified-")
				parts := strings.SplitN(suffix, "-", 2)
				if len(parts) == 2 {
					snap.ModelBucket = parts[0]
					switch parts[1] {
					case "utilization":
						snap.ModelUtilization, _ = strconv.ParseFloat(val, 64)
					case "reset":
						snap.ModelReset, _ = strconv.ParseInt(val, 10, 64)
					}
				}
			}
		}
	}
	return snap
}
