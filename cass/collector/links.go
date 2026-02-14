package collector

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// it2 session patterns for messages (active communication) and observations (reading state).
var it2Patterns = []struct {
	re     *regexp.Regexp
	action string
	kind   string // "message" or "observation"
}{
	{regexp.MustCompile(`it2\s+session\s+send-text\s+["']?([0-9A-Fa-f-]{8,36})["']?\s+["'](.+?)["']`), "send-text", "message"},
	{regexp.MustCompile(`it2\s+session\s+send-key\s+["']?([0-9A-Fa-f-]{8,36})["']?\s+["'](.+?)["']`), "send-key", "message"},
	{regexp.MustCompile(`it2\s+session\s+get-screen\s+["']?([0-9A-Fa-f-]{8,36})["']?`), "get-screen", "observation"},
	{regexp.MustCompile(`it2\s+session\s+get-buffer\s+["']?([0-9A-Fa-f-]{8,36})["']?`), "get-buffer", "observation"},
}

// it2 session current response and confirmation patterns for extracting source session ID.
var (
	// Matches: [it2:send-text src=XXXXXXXX dst=YYYYYYYY]
	srcPattern = regexp.MustCompile(`\[it2:(?:send-text|get-screen|get-buffer)\s+src=([0-9A-Fa-f-]{8,36})`)
	// Matches: it2 session current output (just a UUID).
	currentPattern = regexp.MustCompile(`^([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})\s*$`)
)

// ExtractLinks scans session entries for it2 session interactions.
//
// Only assistant tool_use blocks (the agent's own commands) are scanned for
// target session IDs. Tool result confirmations (e.g. [it2:send-text src=XXX dst=YYY])
// are used only to identify the source session ID, not to find new targets.
// This avoids false positives from observing other sessions' output via
// get-screen/get-buffer.
func ExtractLinks(entries []cc.Entry) []cass.SessionLink {
	var links []cass.SessionLink
	seen := make(map[string]bool)
	selfID := "" // This session's iTerm2 ID, if we can determine it.

	// First pass: try to find this session's iTerm2 ID.
	for _, e := range entries {
		if selfID != "" {
			break
		}
		// Look in tool results for [it2:... src=XXX] confirmations.
		if e.ToolUseResult != nil && e.ToolUseResult.Stdout != "" {
			if m := srcPattern.FindStringSubmatch(e.ToolUseResult.Stdout); len(m) > 1 {
				selfID = m[1]
			}
		}
		// Look for `it2 session current` output.
		if e.ToolUseResult != nil && e.ToolUseResult.Stdout != "" {
			if m := currentPattern.FindStringSubmatch(strings.TrimSpace(e.ToolUseResult.Stdout)); len(m) > 1 {
				selfID = m[1]
			}
		}
	}

	// Second pass: extract links from assistant tool_use blocks.
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "assistant" {
			continue
		}

		blocks := e.Message.ContentBlocks()
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "Bash" {
				cmd := extractBashCommand(b.Input)
				found := findLinks(cmd, e, seen)
				for i := range found {
					if found[i].SourceSession == "" {
						found[i].SourceSession = selfID
					}
				}
				links = append(links, found...)
			}
		}
	}

	return links
}

func findLinks(text string, e cc.Entry, seen map[string]bool) []cass.SessionLink {
	var links []cass.SessionLink
	for _, pat := range it2Patterns {
		matches := pat.re.FindAllStringSubmatch(text, -1)
		for _, m := range matches {
			target := m[1]
			key := pat.action + ":" + target
			if seen[key] {
				continue
			}
			seen[key] = true

			link := cass.SessionLink{
				TargetSession: target,
				Kind:          pat.kind,
				Action:        pat.action,
			}
			if !e.Timestamp.IsZero() {
				link.Timestamp = e.Timestamp.Format(tsFormat)
			}
			// send-text and send-key have message content in capture group 2.
			if pat.kind == "message" && len(m) > 2 {
				msg := m[2]
				if len(msg) > 200 {
					msg = msg[:200] + "..."
				}
				link.Text = msg
			}
			links = append(links, link)
		}
	}
	return links
}

// extractItermSessionID finds this session's iTerm2 session ID from entries.
func extractItermSessionID(entries []cc.Entry) string {
	for _, e := range entries {
		if e.ToolUseResult == nil || e.ToolUseResult.Stdout == "" {
			continue
		}
		stdout := e.ToolUseResult.Stdout
		// Check for [it2:... src=XXX] confirmation.
		if m := srcPattern.FindStringSubmatch(stdout); len(m) > 1 {
			return m[1]
		}
		// Check for `it2 session current` output.
		if m := currentPattern.FindStringSubmatch(strings.TrimSpace(stdout)); len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func extractBashCommand(raw json.RawMessage) string {
	// Bash tool input is {"command": "..."} or might be a string.
	var obj struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Command != "" {
		return obj.Command
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// Fall back to scanning the raw JSON for it2 patterns.
	return strings.ReplaceAll(string(raw), `\"`, `"`)
}
