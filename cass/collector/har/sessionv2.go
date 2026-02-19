package har

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/misc/proxyman"
	"github.com/tmc/misc/proxyman/types"

	"github.com/tmc/cc/cass"
)

// ParseSessionV2File reads a .proxymansessionv2 or .proxymanlogv2 file and returns
// parsed APIRequests. Only Anthropic Messages API entries are returned.
func ParseSessionV2File(path string) ([]cass.APIRequest, error) {
	r := proxyman.NewReader()
	lf, err := r.Read(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	entries, err := lf.Entries()
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var results []cass.APIRequest
	for i := range entries {
		req := convertSessionV2Entry(&entries[i], path)
		if req == nil {
			continue
		}
		results = append(results, *req)
	}
	return results, nil
}

// ScanSessionV2Dir walks a directory for .proxymansessionv2 and .proxymanlogv2
// files and returns parsed APIRequests. Non-Anthropic entries are silently skipped.
func ScanSessionV2Dir(dir string) ([]cass.APIRequest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}

	var all []cass.APIRequest
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".proxymansessionv2") && !strings.HasSuffix(name, ".proxymanlogv2") {
			continue
		}
		reqs, err := ParseSessionV2File(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		all = append(all, reqs...)
	}
	return all, nil
}

// convertSessionV2Entry converts a proxyman types.Entry to a cass.APIRequest.
// Returns nil if the entry is not an Anthropic Messages API call.
func convertSessionV2Entry(e *types.Entry, sourcePath string) *cass.APIRequest {
	if !strings.Contains(e.URL, "anthropic.com") {
		return nil
	}
	if !strings.Contains(e.URL, "/v1/messages") || strings.Contains(e.URL, "count_tokens") {
		return nil
	}

	req := &cass.APIRequest{
		SourceFile:   sourcePath,
		StatusCode:   e.StatusCode,
		DurationMs:   int(e.Duration.Milliseconds()),
		ClientPID:    e.ClientPid,
	}

	if !e.Timestamp.IsZero() && e.Timestamp.Year() > 2000 {
		req.Timestamp = e.Timestamp.Unix()
	}

	requestID := headerValueMap(e.ResponseHeaders, "request-id")
	req.RequestID = requestID
	req.SourceHash = computeSourceHash(requestID, sourcePath)
	req.ID = computeID(requestID, req.Timestamp)

	if e.RequestBody != "" {
		parseRequestBody(e.RequestBody, req)
	}

	// Response body is raw SSE (same as NDJSON artifact format — not base64).
	if e.ResponseBody != "" {
		parseResponseBody(&Content{Text: e.ResponseBody}, req)
	}

	req.RateLimits = extractRateLimitsMap(e.ResponseHeaders, req.Timestamp)
	req.OrgID = headerValueMap(e.ResponseHeaders, "x-organization-id")

	return req
}
