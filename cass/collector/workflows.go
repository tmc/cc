package collector

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

var workflowMetaRE = regexp.MustCompile(`(?m)\b(name|description)\s*:\s*['"]([^'"]+)['"]`)

// ExtractWorkflows summarizes native Claude Code Workflow runs for a parent
// session. It uses the parent JSONL for launch metadata and the workflow state
// files on disk for completion and fan-out counts.
func ExtractWorkflows(sessionPath string, entries []cc.Entry) []cass.WorkflowRun {
	byRun := map[string]*cass.WorkflowRun{}
	var pending []cass.WorkflowRun

	for _, e := range entries {
		if e.Attachment != nil && e.Attachment.Type == "workflow_keyword_request" {
			continue
		}
		if e.Message == nil {
			if e.ToolUseResult != nil {
				applyWorkflowToolResult(byRun, &pending, e.ToolUseResult, e.Timestamp)
			}
			continue
		}
		for _, b := range e.Message.ToolUses() {
			if b.Name != "Workflow" {
				continue
			}
			w := workflowFromToolUse(b)
			w.StartedAt = e.Timestamp
			pending = append(pending, w)
		}
		if e.ToolUseResult != nil {
			applyWorkflowToolResult(byRun, &pending, e.ToolUseResult, e.Timestamp)
		}
		for _, b := range e.Message.ToolResults() {
			if b.ToolUseID == "" {
				continue
			}
			var raw map[string]any
			if err := json.Unmarshal([]byte(b.Content), &raw); err == nil {
				if tr := workflowResultFromMap(raw); tr != nil {
					applyWorkflowToolResult(byRun, &pending, tr, e.Timestamp)
				}
			}
		}
	}

	readWorkflowState(sessionPath, byRun)

	if len(byRun) == 0 {
		return nil
	}
	out := make([]cass.WorkflowRun, 0, len(byRun))
	for _, w := range byRun {
		if w.TranscriptDir != "" {
			w.AgentCount = countWorkflowAgents(w.TranscriptDir)
			w.JournalEventCount = countWorkflowJournalLines(filepath.Join(w.TranscriptDir, "journal.jsonl"))
		}
		out = append(out, *w)
	}
	return out
}

func workflowFromToolUse(b cc.ContentBlock) cass.WorkflowRun {
	var input struct {
		Script             string `json:"script"`
		ScriptPath         string `json:"scriptPath"`
		ResumeFromRunID    string `json:"resumeFromRunId"`
		ResumeFromRunIDAlt string `json:"resume_from_run_id"`
	}
	_ = json.Unmarshal(b.Input, &input)
	w := cass.WorkflowRun{ScriptPath: input.ScriptPath}
	if input.ResumeFromRunID != "" {
		w.RunID = input.ResumeFromRunID
	} else if input.ResumeFromRunIDAlt != "" {
		w.RunID = input.ResumeFromRunIDAlt
	}
	for _, m := range workflowMetaRE.FindAllStringSubmatch(input.Script, -1) {
		switch m[1] {
		case "name":
			if w.Name == "" {
				w.Name = m[2]
			}
		case "description":
			if w.Description == "" {
				w.Description = m[2]
			}
		}
	}
	return w
}

func applyWorkflowToolResult(byRun map[string]*cass.WorkflowRun, pending *[]cass.WorkflowRun, tr *cc.ToolUseResult, ts time.Time) {
	if tr == nil || tr.Type == "" && tr.TaskID == "" {
		return
	}
	if tr.Status != "async_launched" && tr.TaskID == "" {
		return
	}
	runID := workflowRunIDFromResult(tr)
	if runID == "" {
		return
	}
	w := byRun[runID]
	if w == nil {
		var base cass.WorkflowRun
		if len(*pending) > 0 {
			base = (*pending)[0]
			*pending = (*pending)[1:]
		}
		w = &base
		w.RunID = runID
		byRun[runID] = w
	}
	if w.StartedAt.IsZero() {
		w.StartedAt = ts
	}
	w.TaskID = firstNonEmpty(w.TaskID, tr.TaskID)
	w.Status = firstNonEmpty(w.Status, tr.Status)
	w.Summary = firstNonEmpty(w.Summary, workflowSummary(tr))
	w.ScriptPath = firstNonEmpty(w.ScriptPath, tr.ScriptPath)
	w.TranscriptDir = firstNonEmpty(w.TranscriptDir, tr.TranscriptDir)
}

func workflowRunIDFromResult(tr *cc.ToolUseResult) string {
	if tr.RunID != "" {
		return tr.RunID
	}
	if strings.Contains(tr.Stdout, "Run ID:") {
		for _, line := range strings.Split(tr.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Run ID:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "Run ID:"))
			}
		}
	}
	return ""
}

func workflowSummary(tr *cc.ToolUseResult) string {
	if tr.Summary != "" {
		return tr.Summary
	}
	for _, line := range strings.Split(tr.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Summary:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Summary:"))
		}
	}
	return ""
}

func workflowResultFromMap(raw map[string]any) *cc.ToolUseResult {
	b, _ := json.Marshal(raw)
	var tr cc.ToolUseResult
	if err := json.Unmarshal(b, &tr); err != nil {
		return nil
	}
	return &tr
}

func readWorkflowState(sessionPath string, byRun map[string]*cass.WorkflowRun) {
	dir := filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "workflows")
	infos, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, fi := range infos {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, fi.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw struct {
			RunID      string          `json:"runId"`
			TaskID     string          `json:"taskId"`
			Timestamp  time.Time       `json:"timestamp"`
			Script     string          `json:"script"`
			ScriptPath string          `json:"scriptPath"`
			Result     json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &raw) != nil || raw.RunID == "" {
			continue
		}
		w := byRun[raw.RunID]
		if w == nil {
			w = &cass.WorkflowRun{RunID: raw.RunID}
			byRun[raw.RunID] = w
		}
		w.TaskID = firstNonEmpty(w.TaskID, raw.TaskID)
		w.ScriptPath = firstNonEmpty(w.ScriptPath, raw.ScriptPath)
		if w.StartedAt.IsZero() {
			w.StartedAt = raw.Timestamp
		}
		for _, m := range workflowMetaRE.FindAllStringSubmatch(raw.Script, -1) {
			if m[1] == "name" && w.Name == "" {
				w.Name = m[2]
			}
			if m[1] == "description" && w.Description == "" {
				w.Description = m[2]
			}
		}
		if len(raw.Result) > 0 && string(raw.Result) != "null" {
			w.Status = "completed"
			w.CompletedAt = raw.Timestamp
		}
		w.SourcePath = path
		if w.TranscriptDir == "" {
			w.TranscriptDir = filepath.Join(strings.TrimSuffix(sessionPath, ".jsonl"), "subagents", "workflows", raw.RunID)
		}
	}
}

func countWorkflowAgents(dir string) int {
	infos, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, fi := range infos {
		if !fi.IsDir() && strings.HasPrefix(fi.Name(), "agent-") && strings.HasSuffix(fi.Name(), ".jsonl") {
			n++
		}
	}
	return n
}

func countWorkflowJournalLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	return n
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
