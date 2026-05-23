package har

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/cass"
)

type sessionV2Entry struct {
	Timestamp       time.Time
	URL             string
	StatusCode      int
	RequestHeaders  map[string][]string
	ResponseHeaders map[string][]string
	RequestBody     string
	ResponseBody    string
	Duration        time.Duration
	ClientPid       int
}

// ParseSessionV2File reads a .proxymansessionv2 or .proxymanlogv2 file and returns
// parsed APIRequests. Only Anthropic Messages API entries are returned.
func ParseSessionV2File(path string) ([]cass.APIRequest, error) {
	entries, err := parseSessionV2Entries(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
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

// convertSessionV2Entry converts a Proxyman session entry to a cass.APIRequest.
// Returns nil if the entry is not an Anthropic Messages API call.
func convertSessionV2Entry(e *sessionV2Entry, sourcePath string) *cass.APIRequest {
	if !strings.Contains(e.URL, "anthropic.com") {
		return nil
	}
	if !strings.Contains(e.URL, "/v1/messages") || strings.Contains(e.URL, "count_tokens") {
		return nil
	}

	req := &cass.APIRequest{
		SourceFile: sourcePath,
		StatusCode: e.StatusCode,
		DurationMs: int(e.Duration.Milliseconds()),
		ClientPID:  e.ClientPid,
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

func parseSessionV2Entries(path string) ([]sessionV2Entry, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open proxyman archive: %w", err)
	}
	defer zr.Close()

	for _, file := range zr.File {
		if file.Name == "SharedFlowStorage" {
			return parseSharedFlowStorage(file)
		}
	}

	var entries []sessionV2Entry
	for _, file := range zr.File {
		if !strings.HasPrefix(file.Name, "request_") {
			continue
		}
		data, err := readZipFile(file)
		if err != nil {
			return nil, err
		}
		entry, ok, err := parseProxymanLogEntry(data)
		if err != nil || !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseSharedFlowStorage(file *zip.File) ([]sessionV2Entry, error) {
	data, err := readZipFile(file)
	if err != nil {
		return nil, err
	}

	var flows map[string]any
	if err := json.Unmarshal(data, &flows); err != nil {
		return nil, fmt.Errorf("parse SharedFlowStorage: %w", err)
	}

	keys := make([]string, 0, len(flows))
	for key := range flows {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var entries []sessionV2Entry
	for _, key := range keys {
		flow, ok := flows[key].(map[string]any)
		if !ok {
			continue
		}
		entry, ok := parseSessionFlow(flow)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func readZipFile(file *zip.File) ([]byte, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file.Name, err)
	}
	return data, nil
}

func parseProxymanLogEntry(data []byte) (sessionV2Entry, bool, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return sessionV2Entry{}, false, err
	}

	var entry sessionV2Entry
	if createdAt, ok := numberValue(raw["createdAt"]); ok {
		entry.Timestamp = time.Unix(int64(createdAt), 0)
	} else if startTime, ok := numberValue(raw["startTime"]); ok {
		entry.Timestamp = time.Unix(int64(startTime), 0)
	} else if timestamp, ok := numberValue(raw["timestamp"]); ok {
		entry.Timestamp = time.Unix(int64(timestamp), 0)
	}
	if duration, ok := numberValue(raw["duration"]); ok {
		entry.Duration = time.Duration(duration * float64(time.Millisecond))
	}

	if request, ok := raw["request"].(map[string]any); ok {
		if fullPath, ok := stringValue(request["fullPath"]); ok {
			entry.URL = fullPath
		}
		entry.RequestHeaders = parsePlainHeaders(request["headers"])
		entry.RequestBody = bodyValue(request)
	}

	if response, ok := raw["response"].(map[string]any); ok {
		if status, ok := response["status"].(map[string]any); ok {
			if code, ok := numberValue(status["code"]); ok {
				entry.StatusCode = int(code)
			}
		} else if code, ok := numberValue(response["statusCode"]); ok {
			entry.StatusCode = int(code)
		}
		entry.ResponseHeaders = parsePlainHeaders(response["headers"])
		entry.ResponseBody = bodyValue(response)
	}

	if client, ok := raw["client"].(map[string]any); ok {
		if pid, ok := numberValue(client["pid"]); ok {
			entry.ClientPid = int(pid)
		}
	}

	return entry, entry.URL != "", nil
}

func parseSessionFlow(raw map[string]any) (sessionV2Entry, bool) {
	var entry sessionV2Entry

	if timing, ok := raw["timing"].(map[string]any); ok {
		start, haveStart := numberValue(timing["requestStartedAt"])
		if haveStart {
			entry.Timestamp = time.Unix(int64(start), 0)
		}
		if end, ok := numberValue(timing["requestEndedAt"]); ok && haveStart {
			entry.Duration = time.Duration((end - start) * float64(time.Second))
		}
	}

	if request, ok := raw["request"].(map[string]any); ok {
		if fullPath, ok := stringValue(request["fullPath"]); ok {
			entry.URL = fullPath
		} else if uri, ok := stringValue(request["uri"]); ok {
			if scheme, ok := stringValue(request["scheme"]); ok {
				entry.URL = scheme + "://" + uri
			} else {
				entry.URL = uri
			}
		}
		entry.RequestHeaders = parseSessionHeaders(request)
		entry.RequestBody, _ = stringValue(request["bodyData"])
	}

	if response, ok := raw["response"].(map[string]any); ok {
		if status, ok := response["status"].(map[string]any); ok {
			if code, ok := numberValue(status["code"]); ok {
				entry.StatusCode = int(code)
			}
		}
		entry.ResponseHeaders = parseSessionHeaders(response)
		entry.ResponseBody, _ = stringValue(response["bodyData"])
	}

	if client, ok := raw["client"].(map[string]any); ok {
		if pid, ok := numberValue(client["pid"]); ok {
			entry.ClientPid = int(pid)
		}
	}

	return entry, entry.URL != ""
}

func bodyValue(obj map[string]any) string {
	if body, ok := obj["body"].(map[string]any); ok {
		if text, ok := stringValue(body["text"]); ok {
			return text
		}
		if data, ok := stringValue(body["data"]); ok {
			return data
		}
	}
	if text, ok := stringValue(obj["bodyText"]); ok {
		return text
	}
	if body, ok := stringValue(obj["rawBody"]); ok {
		return body
	}
	return ""
}

func parsePlainHeaders(v any) map[string][]string {
	headers := make(map[string][]string)

	switch h := v.(type) {
	case []any:
		for _, item := range h {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, nameOK := stringValue(obj["name"])
			value, valueOK := stringValue(obj["value"])
			if nameOK && valueOK {
				headers[name] = append(headers[name], value)
			}
		}
	case map[string]any:
		for name, value := range h {
			switch value := value.(type) {
			case string:
				headers[name] = append(headers[name], value)
			case []any:
				for _, item := range value {
					if s, ok := stringValue(item); ok {
						headers[name] = append(headers[name], s)
					}
				}
			}
		}
	}

	return headers
}

func parseSessionHeaders(obj map[string]any) map[string][]string {
	header, ok := obj["header"].(map[string]any)
	if !ok {
		return nil
	}
	entries, ok := header["entries"].([]any)
	if !ok {
		return nil
	}

	headers := make(map[string][]string)
	for _, item := range entries {
		header, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if enabled, ok := header["isEnabled"].(bool); ok && !enabled {
			continue
		}
		key, ok := header["key"].(map[string]any)
		if !ok {
			continue
		}
		name, nameOK := stringValue(key["name"])
		value, valueOK := stringValue(header["value"])
		if nameOK && valueOK {
			headers[name] = append(headers[name], value)
		}
	}
	return headers
}

func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func numberValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
