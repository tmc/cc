package collector

import (
	"encoding/json"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
	"github.com/tmc/tokencount/anthropictokenizer"
)

// tokenCounter is lazily initialized for output token estimation.
var (
	tokenCounterOnce sync.Once
	tokenCounter     *anthropictokenizer.Counter
)

// getTokenCounter returns the shared token counter, or nil if initialization failed.
func getTokenCounter() *anthropictokenizer.Counter {
	tokenCounterOnce.Do(func() {
		c, err := anthropictokenizer.NewCounter()
		if err == nil {
			tokenCounter = c
		}
	})
	return tokenCounter
}

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

// tsFormat is the timestamp format used for link timestamps.
const tsFormat = "2006-01-02T15:04:05Z07:00"

// teammateMessagePattern matches <teammate-message> XML tags in user message content.
// Requires a non-empty teammate_id.
var teammateMessagePattern = regexp.MustCompile(`<teammate-message\s+teammate_id="([^"]+)"`)

// teammateSummaryPattern extracts the summary attribute from teammate-message tags.
var teammateSummaryPattern = regexp.MustCompile(`<teammate-message\s+teammate_id="([^"]+)"[^>]*\bsummary="([^"]*)"`)

// ExtractStats computes session metrics from entries.
func ExtractStats(entries []cc.Entry) cass.SessionStats {
	var s cass.SessionStats
	s.ToolBreakdown = make(map[string]int)

	filesRead := make(map[string]bool)
	filesWritten := make(map[string]bool)
	filesEdited := make(map[string]bool)

	for _, e := range entries {
		if e.Type == "system" && e.Subtype == "compact_boundary" {
			s.Compactions++
		}
		if e.Message == nil {
			continue
		}

		switch e.Message.Role {
		case "user":
			s.Turns++
			if e.PermissionMode == "plan" {
				s.PlanModeTurns++
			}
			// Count incoming teammate messages (non-empty teammate_id only).
			text := e.Message.TextContent()
			for _, m := range teammateMessagePattern.FindAllStringSubmatch(text, -1) {
				if m[1] != "" {
					s.TeamMessagesRecvd++
				}
			}
		case "assistant":
			// Token usage.
			// OutputTokens in JSONL is a streaming-start snapshot (value=1);
			// the final count lives only in SSE message_delta, not in JSONL.
			// We estimate it via BPE tokenization of the text content.
			if e.Message.Usage != nil {
				s.InputTokens += e.Message.Usage.InputTokens
				out := e.Message.Usage.OutputTokens
				// If output is undercounted (streaming-start = 1), estimate from content.
				if out <= 1 {
					if tc := getTokenCounter(); tc != nil {
						if text := e.Message.TextContent(); text != "" {
							out = tc.Count(text)
							s.OutputTokensEstimated = true
						}
					}
				}
				s.OutputTokens += out
				s.CacheReads += e.Message.Usage.CacheReadInputTokens
				s.CacheCreationInputTokens += e.Message.Usage.CacheCreationInputTokens
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
					// Check if this Task spawns a team member.
					if tn := extractTaskTeamName(b.Input); tn != "" {
						s.TeamMembersSpawned++
					}
				case "Bash":
					cmd := extractBashCommand(b.Input)
					countIT2Commands(cmd, &s)
					countTeamCommands(cmd, &s)
				// Agent teams native tool names.
				case "TeamCreate":
					s.TeamSpawns++
				case "SendMessage", "AgentMessage":
					s.TeamInboxSends++
				case "AgentTask":
					s.TeamTaskOps++
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

// ClassifyTeamRole determines whether a session is a team lead, team member,
// or has no native team affiliation. Returns teamName, agentName, isLead.
//
// Detection heuristics (from JSONL data alone):
//   - Lead: has TeamCreate tool use, or has teamName but no agentName
//   - Member: has agentName field set (present on all member entries)
//   - Neither: no teamName/agentName fields
func ClassifyTeamRole(entries []cc.Entry) (teamName, agentName string, isLead bool) {
	hasTeamCreate := false

	for _, e := range entries {
		if e.TeamName != "" && teamName == "" {
			teamName = e.TeamName
		}
		if e.AgentName != "" && agentName == "" {
			agentName = e.AgentName
		}

		// Check for TeamCreate tool use (definitive lead signal).
		if e.Message != nil && e.Message.Role == "assistant" {
			for _, b := range e.Message.ContentBlocks() {
				if b.Type == "tool_use" && b.Name == "TeamCreate" {
					hasTeamCreate = true
				}
			}
		}

		// Early exit once we have all signals.
		if teamName != "" && agentName != "" && hasTeamCreate {
			break
		}
	}

	if teamName == "" {
		return "", "", false
	}

	// Lead sessions: have TeamCreate, or have teamName but no agentName.
	// Member sessions: always have agentName set.
	isLead = hasTeamCreate || (teamName != "" && agentName == "")
	return teamName, agentName, isLead
}

// ExtractTeamLinks extracts inter-agent communication links from team sessions.
// These complement the it2-based links in ExtractLinks.
func ExtractTeamLinks(entries []cc.Entry) []cass.SessionLink {
	var links []cass.SessionLink
	seen := make(map[string]bool)

	teamName, agentName, _ := ClassifyTeamRole(entries)
	if teamName == "" {
		return nil
	}

	// Determine the sender identity.
	sender := agentName
	if sender == "" {
		sender = "team-lead"
	}

	for _, e := range entries {
		if e.Message == nil {
			continue
		}

		ts := ""
		if !e.Timestamp.IsZero() {
			ts = e.Timestamp.Format(tsFormat)
		}

		switch e.Message.Role {
		case "assistant":
			for _, b := range e.Message.ContentBlocks() {
				if b.Type != "tool_use" {
					continue
				}
				switch b.Name {
				case "Task":
					// Team member spawn.
					name := extractTaskMemberName(b.Input)
					tn := extractTaskTeamName(b.Input)
					if name != "" && tn != "" {
						key := "team-spawn:" + name
						if !seen[key] {
							seen[key] = true
							links = append(links, cass.SessionLink{
								SourceSession: sender,
								TargetSession: name,
								Kind:          "team",
								Action:        "team-spawn",
								TeamName:      teamName,
								Timestamp:     ts,
							})
						}
					}
				case "SendMessage", "AgentMessage":
					// Message to another agent.
					recipient := extractRecipient(b.Input)
					if recipient != "" {
						key := "team-message:" + sender + ":" + recipient
						if !seen[key] {
							seen[key] = true
							summary := extractSummary(b.Input)
							if len(summary) > 200 {
								summary = summary[:200] + "..."
							}
							links = append(links, cass.SessionLink{
								SourceSession: sender,
								TargetSession: recipient,
								Kind:          "team",
								Action:        "team-message",
								Text:          summary,
								TeamName:      teamName,
								Timestamp:     ts,
							})
						}
					}
				}
			}
		case "user":
			// Incoming teammate messages — extract summary for display.
			text := e.Message.TextContent()
			summaryByID := map[string]string{}
			for _, m := range teammateSummaryPattern.FindAllStringSubmatch(text, -1) {
				summaryByID[m[1]] = m[2]
			}
			for _, m := range teammateMessagePattern.FindAllStringSubmatch(text, -1) {
				from := m[1]
				key := "team-message:" + from + ":" + sender
				if !seen[key] {
					seen[key] = true
					linkText := summaryByID[from]
					if linkText == "" {
						linkText = "[from " + from + "]"
					}
					if len(linkText) > 200 {
						linkText = linkText[:200] + "..."
					}
					links = append(links, cass.SessionLink{
						SourceSession: from,
						TargetSession: sender,
						Kind:          "team",
						Action:        "team-message",
						Text:          linkText,
						TeamName:      teamName,
						Timestamp:     ts,
					})
				}
			}
		}
	}

	return links
}

// buildSparkline buckets entry timestamps into slots and returns a Unicode sparkline.
func buildSparkline(entries []cc.Entry) string {
	const buckets = 16
	const bars = " ▁▂▃▄▅▆▇█"

	if len(entries) < 2 {
		return ""
	}
	// Find first and last non-zero timestamps.
	var first, last time.Time
	for _, e := range entries {
		if e.Timestamp.IsZero() {
			continue
		}
		if first.IsZero() || e.Timestamp.Before(first) {
			first = e.Timestamp
		}
		if last.IsZero() || e.Timestamp.After(last) {
			last = e.Timestamp
		}
	}
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

func extractTaskTeamName(raw json.RawMessage) string {
	var obj struct {
		TeamName string `json:"team_name"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.TeamName
	}
	return ""
}

func extractTaskMemberName(raw json.RawMessage) string {
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Name
	}
	return ""
}

func extractRecipient(raw json.RawMessage) string {
	var obj struct {
		Recipient string `json:"recipient"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Recipient
	}
	return ""
}

func extractSummary(raw json.RawMessage) string {
	var obj struct {
		Summary string `json:"summary"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Summary
	}
	return ""
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
