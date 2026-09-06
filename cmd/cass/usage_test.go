package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc/cass/store"
)

func TestParseSince(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"12w", 84, false},
		{"30d", 30, false},
		{"0", 0, false},
		{"", 0, false},
		{"4W", 28, false},
		{"12", 0, true},
		{"w", 0, true},
		{"-1w", -7, false},
	}
	for _, tt := range tests {
		got, err := parseSince(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSince(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseSince(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestBar checks that a bar is proportional and that a value too small for a
// whole block still shows, since a repo with 1% of the tokens is not nothing.
func TestBar(t *testing.T) {
	if got := bar(10, 10, 8); got != strings.Repeat("█", 8) {
		t.Errorf("full bar = %q", got)
	}
	if got := bar(0, 10, 8); got != "" {
		t.Errorf("zero bar = %q, want empty", got)
	}
	if got := bar(5, 10, 8); got != strings.Repeat("█", 4) {
		t.Errorf("half bar = %q, want four blocks", got)
	}
	if got := bar(1, 100, 8); got == "" {
		t.Error("1% bar is empty; a partial block should show")
	}
	if got := bar(1, 0, 8); got != "" {
		t.Errorf("bar against a zero max = %q, want empty", got)
	}
}

func TestCompact(t *testing.T) {
	for _, tt := range []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1500, "1.5K"},
		{2_500_000, "2.5M"},
		{45_560_000_000, "45.56B"},
	} {
		if got := compact(tt.in); got != tt.want {
			t.Errorf("compact(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCompactInt(t *testing.T) {
	for in, want := range map[int]string{0: "0", 999: "999", 1000: "1000", 12345: "12 345", 242857: "242 857"} {
		if got := compactInt(in); got != want {
			t.Errorf("compactInt(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("github.com/tmc/cc", 30); got != "github.com/tmc/cc" {
		t.Errorf("short string truncated: %q", got)
	}
	if got := truncate("github.com/tmc/cc", 10); got != "github.co…" {
		t.Errorf("truncate = %q, want github.co…", got)
	}
}

// TestFindRepo checks that -repo accepts a name, a remote, a path fragment or
// the git common dir itself, and that a miss is reported rather than silently
// widening the query to every repository.
func TestFindRepo(t *testing.T) {
	repos := map[string]store.Repo{
		"/a/cc/.git":   {GitCommonDir: "/a/cc/.git", Remote: "github.com/tmc/cc", Name: "github.com/tmc/cc"},
		"/b/docs/.git": {GitCommonDir: "/b/docs/.git", Name: "appledocs"},
	}
	for _, tt := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"/a/cc/.git", "/a/cc/.git", true},
		{"tmc/cc", "/a/cc/.git", true},
		{"appledocs", "/b/docs/.git", true},
		{"/b/docs", "/b/docs/.git", true},
		{"nothing", "", false},
	} {
		got, ok := findRepo(repos, tt.in)
		if ok != tt.ok || got != tt.want {
			t.Errorf("findRepo(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// TestQuartileOf checks the shared intensity scale: zero is its own level and
// the levels are quantiles, so the largest of eight values is 4 and the
// smallest 1.
func TestQuartileOf(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8}
	if got := quartileOf(0, sorted); got != 0 {
		t.Errorf("quartileOf(0) = %d, want 0", got)
	}
	if got := quartileOf(1, sorted); got != 1 {
		t.Errorf("quartileOf(1) = %d, want 1", got)
	}
	if got := quartileOf(8, sorted); got != 4 {
		t.Errorf("quartileOf(8) = %d, want 4", got)
	}
	if got := quartileOf(5, nil); got != 0 {
		t.Errorf("quartileOf with no active values = %d, want 0", got)
	}
}

func TestShortDur(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want string
	}{
		{90 * time.Second, "1m"},
		{95 * time.Minute, "1h 35m"},
		{46*24*time.Hour + 20*time.Hour, "46d 20h"},
	} {
		if got := shortDur(tt.in); got != tt.want {
			t.Errorf("shortDur(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestZoneName(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip(err)
	}
	if got := zoneName(loc); got != "America/Los_Angeles" {
		t.Errorf("zoneName = %q", got)
	}
	if got := zoneName(time.UTC); got != "UTC" {
		t.Errorf("zoneName(UTC) = %q", got)
	}
}

func TestDelta(t *testing.T) {
	if got := delta(102, 100); got < 0.019 || got > 0.021 {
		t.Errorf("delta(102, 100) = %v, want 0.02", got)
	}
	if got := delta(0, 0); got != 0 {
		t.Errorf("delta(0, 0) = %v, want 0", got)
	}
	if got := delta(5, 0); got != 1 {
		t.Errorf("delta(5, 0) = %v, want 1", got)
	}
}

// TestTranscriptSumsSkipsSubagents locks the population the cross-check
// compares. Claude Code's own ledger counts only what the main thread spent,
// so an unfolded sum that counted subagent entries would read above it on
// every day a subagent ran.
func TestTranscriptSumsSkipsSubagents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_HOME", home)
	dir := filepath.Join(home, "projects", "p")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(sidechain bool, tokens int) string {
		b, err := json.Marshal(map[string]any{
			"timestamp":   "2026-08-16T03:00:00Z",
			"isSidechain": sidechain,
			"message": map[string]any{
				"id":    "msg_1",
				"model": "claude-opus-5",
				"usage": map[string]any{"input_tokens": tokens},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	body := line(false, 100) + line(true, 900)
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	byDay, _, err := transcriptSums(context.Background(), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := byDay["2026-08-16"]["claude-opus-5"], 100; got != want {
		t.Fatalf("main-thread sum = %d, want %d", got, want)
	}
	// The same entries bucketed west of UTC fall on the previous day, which is
	// why the check buckets in the ledger's zone rather than the reader's.
	byDay, _, err = transcriptSums(context.Background(), time.FixedZone("PDT", -7*3600))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := byDay["2026-08-16"]; ok {
		t.Fatalf("entry stayed on 2026-08-16 west of UTC: %v", byDay)
	}
}
