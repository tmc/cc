package collector

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// it2 command patterns for stats extraction.
var it2StatsPatterns = []struct {
	re    *regexp.Regexp
	field string
}{
	{regexp.MustCompile(`it2\s+session\s+split`), "splits"},
	{regexp.MustCompile(`it2\s+session\s+send-text`), "sends"},
	{regexp.MustCompile(`it2\s+session\s+send-key`), "sends"},
	{regexp.MustCompile(`it2\s+session\s+get-screen`), "screens"},
	{regexp.MustCompile(`it2\s+session\s+get-buffer`), "buffers"},
	{regexp.MustCompile(`it2\s+session\s+set-badge`), "badges"},
	{regexp.MustCompile(`it2\s+session\s+watch`), "watches"},
}

// Team interaction patterns (claude teams infrastructure).
var teamPatterns = []struct {
	re    *regexp.Regexp
	field string
}{
	{regexp.MustCompile(`ccinbox\s+(read|list)`), "inbox_reads"},
	{regexp.MustCompile(`ccinbox\s+(send|append)`), "inbox_sends"},
	{regexp.MustCompile(`cctask\s+(create|update|list|get)`), "task_ops"},
	{regexp.MustCompile(`ccspawn`), "spawns"},
	{regexp.MustCompile(`ccteam`), "team_ops"},
	// Also catch the programmatic cc package calls via cctl.
	{regexp.MustCompile(`cctl\s+inbox`), "inbox_reads"},
	{regexp.MustCompile(`cctl\s+task`), "task_ops"},
	{regexp.MustCompile(`cctl\s+spawn`), "spawns"},
}

// ExtractStats computes session metrics from entries.
func ExtractStats(entries []cc.Entry) cass.SessionStats {
	var s cass.SessionStats
	s.ToolBreakdown = make(map[string]int)

	filesRead := make(map[string]bool)
	filesWritten := make(map[string]bool)
	filesEdited := make(map[string]bool)

	for _, e := range entries {
		if e.Message == nil {
			continue
		}

		switch e.Message.Role {
		case "user":
			s.Turns++
		case "assistant":
			// Token usage.
			if e.Message.Usage != nil {
				s.InputTokens += e.Message.Usage.InputTokens
				s.OutputTokens += e.Message.Usage.OutputTokens
				s.CacheReads += e.Message.Usage.CacheReadInputTokens
			}

			// Tool use analysis.
			for _, b := range e.Message.ContentBlocks() {
				if b.Type != "tool_use" {
					continue
				}
				s.ToolCalls++
				s.ToolBreakdown[b.Name]++

				switch b.Name {
				case "Read":
					fp := extractFilePath(b.Input)
					if fp != "" {
						filesRead[fp] = true
					}
				case "Write":
					fp := extractFilePath(b.Input)
					if fp != "" {
						filesWritten[fp] = true
					}
					s.LinesWritten += countLines(extractContent(b.Input))
				case "Edit":
					fp := extractFilePath(b.Input)
					if fp != "" {
						filesEdited[fp] = true
					}
					s.LinesWritten += countLines(extractNewString(b.Input))
				case "Task":
					s.SubagentSpawns++
				case "Bash":
					cmd := extractBashCommand(b.Input)
					countIT2Commands(cmd, &s)
					countTeamCommands(cmd, &s)
				}
			}
		}
	}

	s.FilesRead = len(filesRead)
	s.FilesWritten = len(filesWritten)
	s.FilesEdited = len(filesEdited)

	// Duration from first to last entry.
	if len(entries) >= 2 {
		first := entries[0].Timestamp
		last := entries[len(entries)-1].Timestamp
		if !first.IsZero() && !last.IsZero() {
			s.DurationSecs = int(last.Sub(first).Seconds())
		}
	}

	s.Sparkline = buildSparkline(entries)
	return s
}

// buildSparkline buckets entry timestamps into slots and returns a Unicode sparkline.
func buildSparkline(entries []cc.Entry) string {
	const buckets = 16
	const bars = " ▁▂▃▄▅▆▇█"

	if len(entries) < 2 {
		return ""
	}
	first := entries[0].Timestamp
	last := entries[len(entries)-1].Timestamp
	if first.IsZero() || last.IsZero() || !last.After(first) {
		return ""
	}

	span := last.Sub(first)
	counts := make([]int, buckets)
	for _, e := range entries {
		if e.Timestamp.IsZero() {
			continue
		}
		pos := int(e.Timestamp.Sub(first) * time.Duration(buckets) / span)
		if pos >= buckets {
			pos = buckets - 1
		}
		counts[pos]++
	}

	// Find max for scaling.
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	if max == 0 {
		return ""
	}

	barsRunes := []rune(bars)
	levels := len(barsRunes) - 1
	var sb strings.Builder
	for _, c := range counts {
		level := c * levels / max
		sb.WriteRune(barsRunes[level])
	}
	return sb.String()
}

func countIT2Commands(cmd string, s *cass.SessionStats) {
	for _, p := range it2StatsPatterns {
		matches := p.re.FindAllString(cmd, -1)
		n := len(matches)
		if n == 0 {
			continue
		}
		switch p.field {
		case "splits":
			s.IT2Splits += n
		case "sends":
			s.IT2Sends += n
		case "screens":
			s.IT2Screens += n
		case "buffers":
			s.IT2Buffers += n
		case "badges":
			s.IT2Badges += n
		case "watches":
			s.IT2Watches += n
		}
	}
}

func countTeamCommands(cmd string, s *cass.SessionStats) {
	for _, p := range teamPatterns {
		matches := p.re.FindAllString(cmd, -1)
		n := len(matches)
		if n == 0 {
			continue
		}
		switch p.field {
		case "inbox_reads":
			s.TeamInboxReads += n
		case "inbox_sends":
			s.TeamInboxSends += n
		case "task_ops":
			s.TeamTaskOps += n
		case "spawns":
			s.TeamSpawns += n
		}
	}
}

func extractFilePath(raw json.RawMessage) string {
	var obj struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.FilePath
	}
	return ""
}

func extractContent(raw json.RawMessage) string {
	var obj struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Content
	}
	return ""
}

func extractNewString(raw json.RawMessage) string {
	var obj struct {
		NewString string `json:"new_string"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.NewString
	}
	return ""
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
