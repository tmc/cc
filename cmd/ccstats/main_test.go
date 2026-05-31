package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ccpkg "github.com/tmc/cc"
)

var updateGoldenFlag = flag.Bool("update", false, "update golden files")

func TestStatsForFileCountsMessagesToolsTokensAndCompactions(t *testing.T) {
	path := writeStatsFixture(t)

	got, err := statsForFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if got.SessionID != "session-123" {
		t.Fatalf("session id = %q, want session-123", got.SessionID)
	}
	if got.Slug != "session-123" {
		t.Fatalf("slug = %q, want session-123", got.Slug)
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Fatalf("model = %q, want claude-sonnet-4-6", got.Model)
	}
	if got.TotalEntries != 4 {
		t.Fatalf("total entries = %d, want 4", got.TotalEntries)
	}
	if got.UserMessages != 1 {
		t.Fatalf("user messages = %d, want 1", got.UserMessages)
	}
	if got.AsstMessages != 1 {
		t.Fatalf("assistant messages = %d, want 1", got.AsstMessages)
	}
	if got.Compactions != 1 {
		t.Fatalf("compactions = %d, want 1", got.Compactions)
	}
	if got.InputTokens != 111 || got.OutputTokens != 22 || got.CacheReadTokens != 3 || got.CacheCreateTokens != 7 {
		t.Fatalf("tokens = %+v, want input=111 output=22 cache_read=3 cache_create=7", got)
	}
	if got.TotalTool != 1 {
		t.Fatalf("total tool uses = %d, want 1", got.TotalTool)
	}
	if got.ToolUses["Bash"] != 1 {
		t.Fatalf("tool uses = %#v, want Bash=1", got.ToolUses)
	}
	if got.Duration != 3*time.Minute {
		t.Fatalf("duration = %s, want 3m", got.Duration)
	}
}

func TestOutputAggregatesSessionsInTextMode(t *testing.T) {
	oldFormat := *formatFlag
	*formatFlag = "text"
	t.Cleanup(func() { *formatFlag = oldFormat })

	stats := []sessionStats{
		{
			SessionID:         "session-123",
			Slug:              "session-123",
			InputTokens:       111,
			OutputTokens:      22,
			CacheReadTokens:   3,
			CacheCreateTokens: 7,
			UserMessages:      1,
			AsstMessages:      1,
			TotalTool:         1,
			Duration:          2 * time.Minute,
			Compactions:       1,
			ToolUses:          map[string]int{"Bash": 1},
		},
		{
			SessionID:         "session-456",
			Slug:              "session-456",
			InputTokens:       10,
			OutputTokens:      5,
			CacheReadTokens:   1,
			CacheCreateTokens: 2,
			UserMessages:      2,
			AsstMessages:      3,
			TotalTool:         4,
			Duration:          30 * time.Second,
			ToolUses:          map[string]int{"Read": 4},
		},
	}

	got := captureStdout(t, func() error {
		return output(stats)
	})
	for _, want := range []string{
		"session-123",
		"session-456",
		"TOTAL (2 sessions)",
		"in:121",
		"out:27",
		"cache_r:4",
		"cache_w:9",
		"tools:5",
		"msgs:3/4",
		"Tool usage:",
		"  4  Read",
		"  1  Bash",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("text output missing %q:\n%s", want, got)
		}
	}
}

func TestCharacteristicsUsageMentionsSubcommand(t *testing.T) {
	oldUsage := flag.Usage
	t.Cleanup(func() { flag.Usage = oldUsage })

	configureUsage("characteristics")
	got := captureStderr(t, func() {
		flag.Usage()
	})
	for _, want := range []string{
		"ccstats characteristics [flags] [file...]",
		"Reports cross-session usage characteristics",
		"-unit string",
		"-verbose",
		"ccstats characteristics -since 24h",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("characteristics usage missing %q:\n%s", want, got)
		}
	}
}

func TestDefaultUsageMentionsCharacteristicsSubcommand(t *testing.T) {
	oldUsage := flag.Usage
	t.Cleanup(func() { flag.Usage = oldUsage })

	configureUsage("sessions")
	got := captureStderr(t, func() {
		flag.Usage()
	})
	if !strings.Contains(got, "ccstats characteristics -since 24h") {
		t.Fatalf("default usage missing characteristics example:\n%s", got)
	}
}

func TestParallelIndexCountsActiveSessions(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	var idx ParallelIndex
	idx.Add("a", base, 2*time.Minute)
	idx.Add("b", base.Add(1*time.Minute), 2*time.Minute)
	idx.Add("c", base.Add(90*time.Second), 2*time.Minute)
	idx.Build()

	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{name: "first request", at: base},
		{name: "two active", at: base.Add(90 * time.Second)},
		{name: "after window", at: base.Add(3 * time.Minute)},
	} {
		want := naiveParallelActiveAt([]parallelWindow{
			{session: "a", start: base, end: base.Add(2 * time.Minute)},
			{session: "b", start: base.Add(1 * time.Minute), end: base.Add(3 * time.Minute)},
			{session: "c", start: base.Add(90 * time.Second), end: base.Add(3*time.Minute + 30*time.Second)},
		}, tc.at)
		if got := idx.ActiveAt(tc.at); got != want {
			t.Fatalf("%s: ActiveAt(%s) = %d, want %d", tc.name, tc.at.Format(time.RFC3339), got, want)
		}
	}
}

func writeStatsFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	toolInput := json.RawMessage(`{"command":"echo hello"}`)
	assistantContent, err := json.Marshal([]ccpkg.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "tool_use", Name: "Bash", Input: toolInput},
	})
	if err != nil {
		t.Fatal(err)
	}
	toolResultContent, err := json.Marshal([]ccpkg.ContentBlock{
		{Type: "tool_result", ToolUseID: "toolu_1", Content: "done"},
	})
	if err != nil {
		t.Fatal(err)
	}

	entries := []ccpkg.Entry{
		{
			Type:      "user",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "user",
				Content: json.RawMessage(`"hello"`),
			},
		},
		{
			Type:      "assistant",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 1, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "assistant",
				Model:   "claude-sonnet-4-6",
				Content: assistantContent,
				Usage: &ccpkg.Usage{
					InputTokens:              111,
					OutputTokens:             22,
					CacheReadInputTokens:     3,
					CacheCreationInputTokens: 7,
				},
			},
		},
		{
			Type:      "system",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 2, 0, 0, time.UTC),
			Subtype:   "compact_boundary",
		},
		{
			Type:      "user",
			SessionID: "session-123",
			Slug:      "session-123",
			Timestamp: time.Date(2026, 5, 31, 12, 3, 0, 0, time.UTC),
			Message: &ccpkg.Message{
				Role:    "user",
				Content: toolResultContent,
			},
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnalyzeCharacteristicsReportsCoreMetrics(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
	})
	*unitFlag = "requests"
	*verboseFlag = true
	*formatFlag = "text"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour

	files := writeCharacteristicsFixture(t)
	report, err := analyzeCharacteristics(files)
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Sessions != 4 {
		t.Fatalf("sessions = %d, want 4", report.Totals.Sessions)
	}
	if report.Totals.Requests != 5 {
		t.Fatalf("requests = %d, want 5", report.Totals.Requests)
	}
	for _, key := range []string{"parallel4+", "context150k+", "subagentHeavy", "longRunning8h+"} {
		if got := report.Characteristics[key].Pct; got <= 0 {
			t.Fatalf("characteristic %s pct = %f, want > 0", key, got)
		}
	}
	if report.Characteristics["parallel4+"].Pct < 0.7 {
		t.Fatalf("parallel4+ pct = %f, want heavy parallel share", report.Characteristics["parallel4+"].Pct)
	}
}

func TestAnalyzeCharacteristicsKeepsNoUsageRequestsInSessionCounts(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
	})
	*unitFlag = "cost"
	*verboseFlag = true
	*formatFlag = "text"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour

	dir := t.TempDir()
	path := writeCharacteristicsSession(t, dir, "missing-usage.jsonl", []ccpkg.Entry{
		{
			Type:        "assistant",
			SessionID:   "session-u",
			Slug:        "session-u",
			Timestamp:   time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			IsSidechain: true,
			Message: &ccpkg.Message{
				Role:    "assistant",
				Model:   "claude-sonnet-4-6",
				Content: toolBlocks("", 0),
			},
		},
		{
			Type:        "assistant",
			SessionID:   "session-u",
			Slug:        "session-u",
			Timestamp:   time.Date(2026, 5, 31, 12, 1, 0, 0, time.UTC),
			IsSidechain: false,
			Message: &ccpkg.Message{
				Role:    "assistant",
				Model:   "claude-sonnet-4-6",
				Content: toolBlocks("", 0),
				Usage: &ccpkg.Usage{
					InputTokens:              100,
					OutputTokens:             10,
					CacheReadInputTokens:     5,
					CacheCreationInputTokens: 2,
				},
			},
		},
	})

	report, err := analyzeCharacteristics([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Requests != 2 {
		t.Fatalf("requests = %d, want 2", report.Totals.Requests)
	}
	if report.Totals.Weight <= 0 {
		t.Fatalf("total weight = %f, want > 0", report.Totals.Weight)
	}
	if got := report.Characteristics["subagentHeavy"].Weight; got <= 0 {
		t.Fatalf("subagentHeavy weight = %f, want > 0", got)
	}
	if got := report.Characteristics["subagentHeavy"].Pct; got <= 0 {
		t.Fatalf("subagentHeavy pct = %f, want > 0", got)
	}
}

func TestAnalyzeCharacteristicsWidensParallelWindowForShortSince(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong, oldSince := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag, *sinceFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
		*sinceFlag = oldSince
	})
	*unitFlag = "requests"
	*verboseFlag = false
	*formatFlag = "text"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour
	*sinceFlag = "1h"

	dir := t.TempDir()
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	path := writeCharacteristicsSession(t, dir, "parallel.jsonl", []ccpkg.Entry{
		assistantEntry("session-a", "session-a", "", base.Add(0*time.Minute), false, "claude-sonnet-4-6", 10, 1, 0, 0, toolBlocks("", 0)),
		assistantEntry("session-b", "session-b", "", base.Add(3*time.Minute), false, "claude-sonnet-4-6", 10, 1, 0, 0, toolBlocks("", 0)),
		assistantEntry("session-c", "session-c", "", base.Add(4*time.Minute), false, "claude-sonnet-4-6", 10, 1, 0, 0, toolBlocks("", 0)),
		assistantEntry("session-d", "session-d", "", base.Add(4*time.Minute+30*time.Second), false, "claude-sonnet-4-6", 10, 1, 0, 0, toolBlocks("", 0)),
	})

	report, err := analyzeCharacteristics([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Characteristics["parallel4+"].Weight; got <= 0 {
		t.Fatalf("parallel4+ weight = %f, want > 0 when short since widens to 5m", got)
	}
	if got := effectiveParallelWindow(); got != 5*time.Minute {
		t.Fatalf("effectiveParallelWindow = %s, want 5m", got)
	}
}

func TestRunCharacteristicsVerboseJSON(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
	})
	*unitFlag = "cost"
	*verboseFlag = true
	*formatFlag = "json"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour

	files := writeCharacteristicsFixture(t)
	gotJSON := captureStdout(t, func() error {
		return runCharacteristics(files)
	})

	var report characteristicsReport
	if err := json.Unmarshal([]byte(gotJSON), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, gotJSON)
	}
	if report.Unit != "cost" {
		t.Fatalf("unit = %q, want cost", report.Unit)
	}
	if _, ok := report.Characteristics["cacheMissHeavy"]; !ok {
		t.Fatalf("verbose report missing cacheMissHeavy: %#v", report.Characteristics)
	}
	for _, key := range []string{"opus", "sonnet", "haiku"} {
		if _, ok := report.ModelShare[key]; !ok {
			t.Fatalf("model share missing %q: %#v", key, report.ModelShare)
		}
	}
	if report.Totals.Weight <= 0 {
		t.Fatalf("total weight = %f, want > 0", report.Totals.Weight)
	}
}

func TestRunCharacteristicsWarnsOnUnknownModel(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
	})
	*unitFlag = "cost"
	*verboseFlag = true
	*formatFlag = "text"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour

	dir := t.TempDir()
	path := writeCharacteristicsSession(t, dir, "unknown.jsonl", []ccpkg.Entry{
		assistantEntry("session-u", "session-u", "", time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC), false, "mystery-model-1", 50, 10, 0, 0, toolBlocks("", 0)),
	})

	out, errText := captureRunOutput(t, func() error {
		return runCharacteristics([]string{path})
	})
	if !strings.Contains(errText, "unknown models: mystery-model-1") {
		t.Fatalf("stderr missing unknown-model warning:\n%s", errText)
	}
	if !strings.Contains(out, "mystery-model-1") {
		t.Fatalf("stdout missing model-derived report:\n%s", out)
	}
}

func TestRunCharacteristicsWarnsOnUnknownModelInJSON(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
	})
	*unitFlag = "cost"
	*verboseFlag = true
	*formatFlag = "json"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour

	dir := t.TempDir()
	path := writeCharacteristicsSession(t, dir, "unknown.jsonl", []ccpkg.Entry{
		assistantEntry("session-u", "session-u", "", time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC), false, "mystery-model-1", 50, 10, 0, 0, toolBlocks("", 0)),
	})

	out, errText := captureRunOutput(t, func() error {
		return runCharacteristics([]string{path})
	})
	if !strings.Contains(errText, "unknown models: mystery-model-1") {
		t.Fatalf("stderr missing unknown-model warning:\n%s", errText)
	}
	var report characteristicsReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("decode JSON report: %v\n%s", err, out)
	}
	if report.Unit != "cost" {
		t.Fatalf("unit = %q, want cost", report.Unit)
	}
}

func TestCharacteristicsGolden(t *testing.T) {
	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldParallel, oldContext, oldLong, oldSince := *parallelWindowFlag, *contextThresholdFlag, *longRunningThresholdFlag, *sinceFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*parallelWindowFlag = oldParallel
		*contextThresholdFlag = oldContext
		*longRunningThresholdFlag = oldLong
		*sinceFlag = oldSince
	})
	*unitFlag = "cost"
	*verboseFlag = false
	*formatFlag = "text"
	*parallelWindowFlag = 2 * time.Minute
	*contextThresholdFlag = 150000
	*longRunningThresholdFlag = 8 * time.Hour
	*sinceFlag = "24h"

	files := writeCharacteristicsFixture(t)
	textOut, jsonOut := captureCharacteristicsOutputs(t, files)

	assertGolden(t, filepath.Join("testdata", "characteristics.golden.txt"), textOut)
	assertGolden(t, filepath.Join("testdata", "characteristics.golden.json"), jsonOut)
}

func writeCharacteristicsFixture(t *testing.T) []string {
	t.Helper()

	dir := t.TempDir()
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	return []string{
		writeCharacteristicsSession(t, dir, "a.jsonl", []ccpkg.Entry{
			assistantEntry("session-a", "session-a", "/loop nightly", base.Add(1*time.Minute), true, "claude-sonnet-4-6", 160000, 10000, 1000, 500, toolBlocks("ScheduleWakeup", 14)),
			{Type: "system", SessionID: "session-a", Slug: "session-a", Timestamp: base.Add(2 * time.Minute), Subtype: "compact_boundary"},
			assistantEntry("session-a", "session-a", "/loop nightly", base.Add(9*time.Hour+1*time.Minute), true, "claude-sonnet-4-6", 4000, 400, 40, 20, toolBlocks("", 0)),
		}),
		writeCharacteristicsSession(t, dir, "b.jsonl", []ccpkg.Entry{
			assistantEntry("session-b", "session-b", "", base.Add(1*time.Minute), false, "claude-opus-4-7", 900, 120, 10, 5, toolBlocks("", 1)),
		}),
		writeCharacteristicsSession(t, dir, "c.jsonl", []ccpkg.Entry{
			assistantEntry("session-c", "session-c", "", base.Add(1*time.Minute), false, "claude-haiku-4-5", 700, 60, 20, 5, toolBlocks("", 1)),
		}),
		writeCharacteristicsSession(t, dir, "d.jsonl", []ccpkg.Entry{
			assistantEntry("session-d", "session-d", "", base.Add(1*time.Minute), false, "claude-sonnet-4-6", 800, 80, 15, 5, toolBlocks("", 1)),
		}),
	}
}

func writeCharacteristicsSession(t *testing.T, dir, name string, entries []ccpkg.Entry) string {
	t.Helper()

	path := filepath.Join(dir, name)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assistantEntry(sessionID, slug, title string, ts time.Time, sidechain bool, model string, input, output, cacheRead, cacheCreate int, content json.RawMessage) ccpkg.Entry {
	return ccpkg.Entry{
		Type:        "assistant",
		SessionID:   sessionID,
		Slug:        slug,
		CustomTitle: title,
		Timestamp:   ts,
		IsSidechain: sidechain,
		Message: &ccpkg.Message{
			Role:    "assistant",
			Model:   model,
			Content: content,
			Usage: &ccpkg.Usage{
				InputTokens:              input,
				OutputTokens:             output,
				CacheReadInputTokens:     cacheRead,
				CacheCreationInputTokens: cacheCreate,
			},
		},
	}
}

func toolBlocks(toolName string, count int) json.RawMessage {
	blocks := make([]ccpkg.ContentBlock, 0, count+1)
	blocks = append(blocks, ccpkg.ContentBlock{Type: "text", Text: "hello"})
	for i := 0; i < count; i++ {
		name := "Bash"
		if i == 0 && toolName != "" {
			name = toolName
		}
		blocks = append(blocks, ccpkg.ContentBlock{
			Type:  "tool_use",
			Name:  name,
			Input: json.RawMessage(`{"command":"echo hello"}`),
		})
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		panic(err)
	}
	return raw
}

func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	runErr := fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("operation failed: %v", runErr)
	}
	return buf.String()
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	r.Close()
	return buf.String()
}

func captureRunOutput(t *testing.T, fn func() error) (string, string) {
	t.Helper()

	oldStdout := os.Stdout
	oldStderr := os.Stderr
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = outW
	os.Stderr = errW

	runErr := fn()

	outW.Close()
	errW.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	var outBuf, errBuf bytes.Buffer
	if _, err := outBuf.ReadFrom(outR); err != nil {
		t.Fatal(err)
	}
	if _, err := errBuf.ReadFrom(errR); err != nil {
		t.Fatal(err)
	}
	outR.Close()
	errR.Close()

	if runErr != nil {
		t.Fatalf("operation failed: %v", runErr)
	}
	return outBuf.String(), errBuf.String()
}

func captureCharacteristicsOutputs(t *testing.T, files []string) (string, string) {
	t.Helper()

	oldUnit, oldVerbose, oldFormat := *unitFlag, *verboseFlag, *formatFlag
	oldSince := *sinceFlag
	t.Cleanup(func() {
		*unitFlag = oldUnit
		*verboseFlag = oldVerbose
		*formatFlag = oldFormat
		*sinceFlag = oldSince
	})

	*unitFlag = "cost"
	*verboseFlag = false
	*sinceFlag = "24h"

	*formatFlag = "text"
	textOut, errText := captureRunOutput(t, func() error {
		return runCharacteristics(files)
	})
	if errText != "" {
		t.Fatalf("unexpected stderr for text report:\n%s", errText)
	}

	*formatFlag = "json"
	jsonOut, errText := captureRunOutput(t, func() error {
		return runCharacteristics(files)
	})
	if errText != "" {
		t.Fatalf("unexpected stderr for json report:\n%s", errText)
	}

	return textOut, jsonOut
}

func assertGolden(t *testing.T, path, got string) {
	t.Helper()

	if *updateGoldenFlag {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}

type parallelWindow struct {
	session string
	start   time.Time
	end     time.Time
}

func naiveParallelActiveAt(windows []parallelWindow, at time.Time) int {
	active := make(map[string]struct{})
	for _, w := range windows {
		if (w.start.Equal(at) || w.start.Before(at)) && at.Before(w.end) {
			active[w.session] = struct{}{}
		}
	}
	return len(active)
}
