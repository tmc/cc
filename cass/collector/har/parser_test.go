package har

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testHARDir returns the sample HAR directory, skipping the test if unavailable.
func testHARDir(t *testing.T) string {
	dir := "/tmp/claude-317e4407-d212-446e-bd20-ff8a19af9f3a-harfiles"
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("sample HAR dir not available: %s", dir)
	}
	return dir
}

func TestScanDirContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ScanDirContext(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanDirContext error = %v, want context canceled", err)
	}
}

func TestScanArtifactDirsContextCanceled(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session", "proxy-traffic.1.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ScanArtifactDirsContext(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanArtifactDirsContext error = %v, want context canceled", err)
	}
}

func TestParseFile_Classifier(t *testing.T) {
	dir := testHARDir(t)
	// 001.json is a Haiku classifier call.
	req, err := ParseFile(filepath.Join(dir, "001.json"))
	if err != nil {
		t.Fatal(err)
	}

	if req.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model = %q, want claude-haiku-4-5-20251001", req.Model)
	}
	if req.ModelFamily != "haiku" {
		t.Errorf("model_family = %q, want haiku", req.ModelFamily)
	}
	if req.Purpose != "classifier" {
		t.Errorf("purpose = %q, want classifier", req.Purpose)
	}
	if req.SessionID != "317e4407-d212-446e-bd20-ff8a19af9f3a" {
		t.Errorf("session_id = %q, want 317e4407-d212-446e-bd20-ff8a19af9f3a", req.SessionID)
	}
	if req.InputTokens != 294 {
		t.Errorf("input_tokens = %d, want 294", req.InputTokens)
	}
	if req.OutputTokens != 21 {
		t.Errorf("output_tokens = %d, want 21", req.OutputTokens)
	}
	if req.RequestID != "req_011CYHbhCHNH8Gc85N5V6B9o" {
		t.Errorf("request_id = %q, want req_011CYHbhCHNH8Gc85N5V6B9o", req.RequestID)
	}
	if req.StatusCode != 200 {
		t.Errorf("status = %d, want 200", req.StatusCode)
	}
	if req.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", req.StopReason)
	}

	// Rate limits.
	if req.RateLimits.Utilization5h != 0.02 {
		t.Errorf("rl_5h = %f, want 0.02", req.RateLimits.Utilization5h)
	}
	if req.RateLimits.Utilization7d != 0.7 {
		t.Errorf("rl_7d = %f, want 0.7", req.RateLimits.Utilization7d)
	}
	if req.RateLimits.RepresentativeClaim != "five_hour" {
		t.Errorf("rl_claim = %q, want five_hour", req.RateLimits.RepresentativeClaim)
	}
	// Haiku should not have a per-model sub-bucket.
	if req.RateLimits.ModelBucket != "" {
		t.Errorf("model_bucket = %q, want empty (Haiku has no sub-bucket)", req.RateLimits.ModelBucket)
	}
}

func TestParseFile_SonnetResponse(t *testing.T) {
	dir := testHARDir(t)
	// 002.json is a Sonnet response call.
	req, err := ParseFile(filepath.Join(dir, "002.json"))
	if err != nil {
		t.Fatal(err)
	}

	if req.Model != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", req.Model)
	}
	if req.ModelFamily != "sonnet" {
		t.Errorf("model_family = %q, want sonnet", req.ModelFamily)
	}
	if req.Purpose != "response" {
		t.Errorf("purpose = %q, want response", req.Purpose)
	}
	if req.SessionID != "317e4407-d212-446e-bd20-ff8a19af9f3a" {
		t.Errorf("session_id = %q, want 317e4407-d212-446e-bd20-ff8a19af9f3a", req.SessionID)
	}

	// Final token usage from message_delta (not the streaming start values).
	if req.InputTokens != 3 {
		t.Errorf("input_tokens = %d, want 3", req.InputTokens)
	}
	if req.OutputTokens != 26 {
		t.Errorf("output_tokens = %d, want 26", req.OutputTokens)
	}
	if req.CacheCreationTokens != 6040 {
		t.Errorf("cache_creation = %d, want 6040", req.CacheCreationTokens)
	}
	if req.CacheReadTokens != 18231 {
		t.Errorf("cache_read = %d, want 18231", req.CacheReadTokens)
	}

	// Context breakdown (byte counts come from json.RawMessage lengths after
	// unmarshaling the postData.text string; they reflect the original JSON
	// serialization from Claude Code's Node.js client).
	if req.SystemPromptBytes != 15689 {
		t.Errorf("system_bytes = %d, want 15689", req.SystemPromptBytes)
	}
	if req.ToolDefinitionBytes != 67863 {
		t.Errorf("tool_bytes = %d, want 67863", req.ToolDefinitionBytes)
	}
	if req.ConversationBytes != 9234 {
		t.Errorf("conversation_bytes = %d, want 9234", req.ConversationBytes)
	}
	if req.TotalRequestBytes != 93080 {
		t.Errorf("total_request_bytes = %d, want 93080", req.TotalRequestBytes)
	}

	// Rate limits — Sonnet has a per-model sub-bucket.
	if req.RateLimits.ModelBucket != "7d_sonnet" {
		t.Errorf("model_bucket = %q, want 7d_sonnet", req.RateLimits.ModelBucket)
	}
	if req.RateLimits.ModelUtilization != 0.0 {
		t.Errorf("model_util = %f, want 0.0", req.RateLimits.ModelUtilization)
	}
}

func TestParseFile_SecondSonnet(t *testing.T) {
	dir := testHARDir(t)
	// 003.json is the second Sonnet response.
	req, err := ParseFile(filepath.Join(dir, "003.json"))
	if err != nil {
		t.Fatal(err)
	}

	if req.OutputTokens != 24 {
		t.Errorf("output_tokens = %d, want 24", req.OutputTokens)
	}
	if req.CacheCreationTokens != 6074 {
		t.Errorf("cache_creation = %d, want 6074", req.CacheCreationTokens)
	}
	// Conversation grows (includes prior turn).
	if req.ConversationBytes != 9499 {
		t.Errorf("conversation_bytes = %d, want 9499", req.ConversationBytes)
	}
}

func TestScanDir(t *testing.T) {
	dir := testHARDir(t)
	requests, err := ScanDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(requests) != 4 {
		t.Fatalf("got %d requests, want 4", len(requests))
	}

	// All should be from the same session.
	for _, r := range requests {
		if r.SessionID != "317e4407-d212-446e-bd20-ff8a19af9f3a" {
			t.Errorf("request %s: session_id = %q, want 317e4407-...", r.RequestID, r.SessionID)
		}
	}

	// Count by model family.
	families := map[string]int{}
	for _, r := range requests {
		families[r.ModelFamily]++
	}
	if families["haiku"] != 2 {
		t.Errorf("haiku count = %d, want 2", families["haiku"])
	}
	if families["sonnet"] != 2 {
		t.Errorf("sonnet count = %d, want 2", families["sonnet"])
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			"user_abc123_account_def456_session_317e4407-d212-446e-bd20-ff8a19af9f3a",
			"317e4407-d212-446e-bd20-ff8a19af9f3a",
		},
		{"no-session-here", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := extractSessionID(tt.input)
		if got != tt.want {
			t.Errorf("extractSessionID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeModelFamily(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4-6", "sonnet"},
		{"claude-haiku-4-5-20251001", "haiku"},
		{"claude-opus-4-6", "opus"},
		{"some-unknown-model", "unknown"},
	}
	for _, tt := range tests {
		got := normalizeModelFamily(tt.input)
		if got != tt.want {
			t.Errorf("normalizeModelFamily(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
