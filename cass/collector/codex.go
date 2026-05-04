package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// Codex collects sessions from Codex JSONL files.
type Codex struct {
	// Root overrides the default ~/.codex/sessions directory.
	Root string
}

// Name returns the agent slug "codex".
func (c *Codex) Name() string { return "codex" }

// Detect reports whether Codex session data is present on the system.
func (c *Codex) Detect(ctx context.Context) (*cass.DetectionResult, error) {
	root, err := c.root()
	if err != nil {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return &cass.DetectionResult{Agent: c.Name()}, nil
	}
	return &cass.DetectionResult{
		Agent: c.Name(),
		Found: true,
		Paths: []string{root},
	}, nil
}

// Scan walks Codex session paths and sends decoded sessions to out.
// It closes out when scanning completes.
func (c *Codex) Scan(ctx context.Context, config cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)

	paths := config.Paths
	if len(paths) == 0 {
		root, err := c.root()
		if err != nil {
			return err
		}
		paths = []string{root}
	}

	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if err := c.scanDir(ctx, root, config, out); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(root, ".jsonl") {
			continue
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			continue
		}
		sess, err := c.parseSession(root)
		if err != nil {
			continue
		}
		if config.Project != "" && !matchProject(sess, config.Project) {
			continue
		}
		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Codex) scanDir(ctx context.Context, root string, config cass.ScanConfig, out chan<- cass.Session) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !config.Since.IsZero() && info.ModTime().Before(config.Since) {
			return nil
		}

		sess, err := c.parseSession(path)
		if err != nil {
			return nil
		}
		if config.Project != "" && !matchProject(sess, config.Project) {
			return nil
		}

		select {
		case out <- sess:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
}

func (c *Codex) parseSession(path string) (cass.Session, error) {
	entries, err := cc.ReadFile(path)
	if err != nil {
		return cass.Session{}, err
	}
	if len(entries) == 0 {
		return cass.Session{}, fmt.Errorf("empty session: %s", path)
	}

	sum := cc.Summarize(path, entries)
	agent := codexAgent(entries)

	var messages []cass.Message
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		text := strings.TrimSpace(e.Message.TextContent())
		if text == "" {
			continue
		}
		messages = append(messages, cass.Message{
			ID:        e.UUID,
			Role:      e.Message.Role,
			Content:   text,
			CreatedAt: e.Timestamp,
		})
	}

	links := ExtractLinks(entries)
	stats := ExtractStats(entries)
	goals := extractCodexGoals(entries)
	skills := ExtractSkills(entries, agent)

	meta := map[string]any{}
	if sum.Version != "" {
		meta["version"] = sum.Version
	}
	if sum.Model != "" {
		meta["model"] = sum.Model
	}
	if len(links) > 0 {
		meta["session_links"] = links
	}
	if originator, source := codexOriginSource(entries); originator != "" || source != "" {
		if originator != "" {
			meta["originator"] = originator
		}
		if source != "" {
			meta["source"] = source
		}
	}

	id := sum.SessionID
	if id == "" {
		id = sessionID(path)
	}

	return cass.Session{
		ID:         id,
		Agent:      agent,
		Title:      titleFromSummary(sum),
		Workspace:  sum.CWD,
		SourcePath: path,
		StartedAt:  sum.FirstTime,
		EndedAt:    sum.LastTime,
		Messages:   messages,
		Goals:      goals,
		Skills:     skills,
		Stats:      stats,
		Metadata:   meta,
	}, nil
}

func (c *Codex) root() (string, error) {
	if c.Root != "" {
		return c.Root, nil
	}
	ch, err := cc.CodexHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(ch, "sessions"), nil
}

func codexAgent(entries []cc.Entry) string {
	for _, e := range entries {
		switch {
		case e.Originator == "codex_cli_rs" || e.Source == "cli":
			return "codex-cli"
		case e.Originator == "Codex Desktop" || e.Source == "vscode":
			return "codex-app"
		}
	}
	return "codex-cli"
}

func codexOriginSource(entries []cc.Entry) (originator, source string) {
	for _, e := range entries {
		if originator == "" && e.Originator != "" {
			originator = e.Originator
		}
		if source == "" && e.Source != "" {
			source = e.Source
		}
		if originator != "" && source != "" {
			return originator, source
		}
	}
	return originator, source
}

var goalObjectiveRe = regexp.MustCompile(`(?s)<untrusted_objective>\s*(.*?)\s*</untrusted_objective>`)

func extractCodexGoals(entries []cc.Entry) []cass.Goal {
	byObjective := map[string]int{}
	var goals []cass.Goal

	upsert := func(g cass.Goal) {
		g.Objective = strings.TrimSpace(html.UnescapeString(g.Objective))
		if g.Objective == "" {
			return
		}
		if g.Status == "" {
			g.Status = "active"
		}
		if i, ok := byObjective[g.Objective]; ok {
			mergeGoal(&goals[i], g)
			return
		}
		byObjective[g.Objective] = len(goals)
		goals = append(goals, cass.NormalizeGoal(g))
	}

	for _, e := range entries {
		if e.Message != nil && e.Message.Role == "developer" {
			text := e.Message.TextContent()
			if m := goalObjectiveRe.FindStringSubmatch(text); len(m) == 2 {
				g := cass.Goal{
					Objective:      m[1],
					Status:         "active",
					LastObservedAt: e.Timestamp,
				}
				if n, ok := parseGoalIntLine(text, "Tokens used"); ok {
					g.TokensUsed = n
				}
				if n, ok := parseGoalIntLine(text, "Time spent pursuing goal"); ok {
					g.TimeUsedSeconds = n
				}
				if n, ok := parseGoalIntLine(text, "Token budget"); ok {
					g.TokenBudget = &n
				}
				g.CompletionGates = parseGoalPromptGates(text, e.Timestamp)
				upsert(g)
			}
		}
		if e.Message != nil && e.Message.Role == "assistant" && len(goals) > 0 {
			if gates := parseAssistantGoalGates(e.Message.TextContent(), e.Timestamp); len(gates) > 0 {
				upsert(cass.Goal{
					Objective:       goals[len(goals)-1].Objective,
					Status:          goals[len(goals)-1].Status,
					LastObservedAt:  e.Timestamp,
					CompletionGates: gates,
				})
			}
		}
		if e.ToolUseResult != nil && e.ToolUseResult.Stdout != "" {
			if g, ok := parseCodexGoalToolOutput(e.ToolUseResult.Stdout); ok {
				if g.LastObservedAt.IsZero() {
					g.LastObservedAt = e.Timestamp
				}
				upsert(g)
			}
		}
	}
	return goals
}

func parseGoalIntLine(text, key string) (int, bool) {
	prefix := "- " + key + ":"
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		fields := strings.Fields(v)
		if len(fields) == 0 || fields[0] == "none" {
			return 0, false
		}
		n, err := strconv.Atoi(fields[0])
		return n, err == nil
	}
	return 0, false
}

func parseGoalPromptGates(text string, at time.Time) []cass.GoalGate {
	var gates []cass.GoalGate
	inGates := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "completion gates:"),
			strings.HasPrefix(lower, "completion gate:"),
			strings.HasPrefix(lower, "required completion gates:"):
			inGates = true
			continue
		case inGates && isSectionBreak(line):
			inGates = false
			continue
		}
		if !inGates {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		switch {
		case strings.HasPrefix(name, "Time spent pursuing goal:"),
			strings.HasPrefix(name, "Tokens used:"),
			strings.HasPrefix(name, "Token budget:"),
			strings.HasPrefix(name, "Tokens remaining:"):
			continue
		}
		gates = appendGoalGate(gates, cass.GoalGate{
			Name:       name,
			Status:     "required",
			Source:     "developer",
			ObservedAt: at,
		})
	}
	return gates
}

func parseAssistantGoalGates(text string, at time.Time) []cass.GoalGate {
	var gates []cass.GoalGate
	status := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "missing completion gates:"):
			status = "missing"
			continue
		case strings.HasPrefix(lower, "ready evidence/prep:"),
			strings.HasPrefix(lower, "ready artifacts"),
			strings.HasPrefix(lower, "ready evidence"):
			status = "complete"
			continue
		case strings.HasPrefix(lower, "completion status: not achieved"),
			strings.Contains(lower, "still blocked"),
			strings.Contains(lower, "blocked on"):
			name, evidence := blockedGoalGate(trimmed)
			if name == "" {
				continue
			}
			gates = appendGoalGate(gates, cass.GoalGate{
				Name:       name,
				Status:     "blocked",
				Source:     "assistant",
				Evidence:   evidence,
				ObservedAt: at,
			})
			continue
		case status != "" && isSectionBreak(trimmed):
			status = ""
			continue
		}
		if status == "" || !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		gates = appendGoalGate(gates, cass.GoalGate{
			Name:       strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")),
			Status:     status,
			Source:     "assistant",
			ObservedAt: at,
		})
	}
	return gates
}

func isSectionBreak(line string) bool {
	return line != "" && !strings.HasPrefix(line, "- ") && strings.HasSuffix(line, ":")
}

func appendGoalGate(gates []cass.GoalGate, gate cass.GoalGate) []cass.GoalGate {
	gate.Name = strings.TrimSpace(html.UnescapeString(gate.Name))
	if gate.Name == "" {
		return gates
	}
	for i := range gates {
		if gates[i].Name != gate.Name {
			continue
		}
		if gate.Status != "" {
			gates[i].Status = gate.Status
		}
		if gate.Source != "" {
			gates[i].Source = gate.Source
		}
		if gate.Evidence != "" {
			gates[i].Evidence = gate.Evidence
		}
		if !gate.ObservedAt.IsZero() && (gates[i].ObservedAt.IsZero() || gate.ObservedAt.After(gates[i].ObservedAt)) {
			gates[i].ObservedAt = gate.ObservedAt
		}
		return gates
	}
	return append(gates, gate)
}

func blockedGoalGate(line string) (name, evidence string) {
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(lower, "completion status: not achieved"):
		return "Completion status not achieved", line
	case strings.Contains(lower, "still blocked"),
		strings.Contains(lower, "blocked on"):
		return "Blocked precondition", line
	}
	return "", ""
}

func parseCodexGoalToolOutput(text string) (cass.Goal, bool) {
	var out struct {
		Goal struct {
			ThreadID        string `json:"threadId"`
			Objective       string `json:"objective"`
			Status          string `json:"status"`
			TokensUsed      int    `json:"tokensUsed"`
			TimeUsedSeconds int    `json:"timeUsedSeconds"`
			CreatedAt       int64  `json:"createdAt"`
			UpdatedAt       int64  `json:"updatedAt"`
		} `json:"goal"`
		CompletionBudgetReport string `json:"completionBudgetReport"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
		return cass.Goal{}, false
	}
	if out.Goal.Objective == "" {
		return cass.Goal{}, false
	}
	g := cass.Goal{
		ThreadID:               out.Goal.ThreadID,
		Objective:              out.Goal.Objective,
		Status:                 out.Goal.Status,
		TokensUsed:             out.Goal.TokensUsed,
		TimeUsedSeconds:        out.Goal.TimeUsedSeconds,
		CompletionBudgetReport: out.CompletionBudgetReport,
	}
	if out.Goal.CreatedAt > 0 {
		g.CreatedAt = time.Unix(out.Goal.CreatedAt, 0)
	}
	if out.Goal.UpdatedAt > 0 {
		g.UpdatedAt = time.Unix(out.Goal.UpdatedAt, 0)
		g.LastObservedAt = g.UpdatedAt
	}
	return g, true
}

func mergeGoal(dst *cass.Goal, src cass.Goal) {
	if src.ThreadID != "" {
		dst.ThreadID = src.ThreadID
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.TokenBudget != nil {
		dst.TokenBudget = src.TokenBudget
	}
	if src.TokensUsed != 0 {
		dst.TokensUsed = src.TokensUsed
	}
	if src.TimeUsedSeconds != 0 {
		dst.TimeUsedSeconds = src.TimeUsedSeconds
	}
	if !src.CreatedAt.IsZero() {
		dst.CreatedAt = src.CreatedAt
	}
	if !src.UpdatedAt.IsZero() {
		dst.UpdatedAt = src.UpdatedAt
	}
	if !src.LastObservedAt.IsZero() && (dst.LastObservedAt.IsZero() || src.LastObservedAt.After(dst.LastObservedAt)) {
		dst.LastObservedAt = src.LastObservedAt
	}
	if src.CompletionBudgetReport != "" {
		dst.CompletionBudgetReport = src.CompletionBudgetReport
	}
	mergeGoalGates(dst, src.CompletionGates)
	*dst = cass.NormalizeGoal(*dst)
}

func mergeGoalGates(dst *cass.Goal, src []cass.GoalGate) {
	for _, gate := range src {
		dst.CompletionGates = appendGoalGate(dst.CompletionGates, gate)
	}
}
