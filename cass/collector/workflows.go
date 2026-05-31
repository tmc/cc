package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

var (
	workflowMetaRE         = regexp.MustCompile(`(?m)\b(name|description)\s*:\s*['"]([^'"]+)['"]`)
	workflowPhaseRE        = regexp.MustCompile(`(?s)\{\s*title\s*:\s*['"]([^'"]+)['"]\s*,\s*detail\s*:\s*['"]([^'"]*)['"]\s*\}`)
	workflowPhaseCallRE    = regexp.MustCompile(`\bphase\(\s*['"]([^'"]+)['"]\s*\)`)
	workflowLensKeyRE      = regexp.MustCompile(`\bkey\s*:\s*['"]([^'"]+)['"]`)
	workflowLabelKeyRE     = regexp.MustCompile("label\\s*:\\s*`([^`]*\\$\\{l\\.key\\}[^`]*)`\\s*,\\s*phase\\s*:\\s*['\"]([^'\"]+)['\"]([^}]*)")
	workflowPhaseKeyRE     = regexp.MustCompile("phase\\s*:\\s*['\"]([^'\"]+)['\"][^}]*label\\s*:\\s*`([^`]*\\$\\{l\\.key\\}[^`]*)`([^}]*)")
	workflowLabelLiteralRE = regexp.MustCompile(`label\s*:\s*['"]([^'"]+)['"]\s*,\s*phase\s*:\s*['"]([^'"]+)['"]([^}]*)`)
	workflowPhaseLiteralRE = regexp.MustCompile(`phase\s*:\s*['"]([^'"]+)['"][^}]*label\s*:\s*['"]([^'"]+)['"]([^}]*)`)
	workflowAgentTypeRE    = regexp.MustCompile(`agentType\s*:\s*['"]([^'"]+)['"]`)
	workflowStressLensRE   = regexp.MustCompile(`reviewer using the "([^"]+)" lens`)
	workflowLensTitleRE    = regexp.MustCompile(`(?m)^LENS:\s*([^\n.]+)`)
)

type workflowAgentSpec struct {
	Label     string
	Phase     string
	AgentType string
}

type workflowScriptInfo struct {
	Name        string
	Description string
	Phases      []cass.WorkflowPhase
	LensKeys    []string
	AgentSpecs  []workflowAgentSpec
}

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
		script := enrichWorkflowFromScriptFile(w)
		if w.TranscriptDir != "" {
			w.Agents = readWorkflowAgents(*w, script)
			w.AgentCount = len(w.Agents)
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
	applyWorkflowScriptInfo(&w, workflowScriptInfoFromScript(input.Script))
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
		applyWorkflowScriptInfo(w, workflowScriptInfoFromScript(raw.Script))
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

func enrichWorkflowFromScriptFile(w *cass.WorkflowRun) workflowScriptInfo {
	if w == nil || w.ScriptPath == "" {
		return workflowScriptInfo{}
	}
	b, err := os.ReadFile(w.ScriptPath)
	if err != nil {
		return workflowScriptInfo{}
	}
	info := workflowScriptInfoFromScript(string(b))
	applyWorkflowScriptInfo(w, info)
	return info
}

func applyWorkflowScriptInfo(w *cass.WorkflowRun, info workflowScriptInfo) {
	if w == nil {
		return
	}
	if w.Name == "" {
		w.Name = info.Name
	}
	if w.Description == "" {
		w.Description = info.Description
	}
	if len(w.Phases) == 0 && len(info.Phases) > 0 {
		w.Phases = info.Phases
	}
}

func workflowScriptInfoFromScript(script string) workflowScriptInfo {
	var info workflowScriptInfo
	if script == "" {
		return info
	}
	for _, m := range workflowMetaRE.FindAllStringSubmatch(script, -1) {
		switch m[1] {
		case "name":
			if info.Name == "" {
				info.Name = m[2]
			}
		case "description":
			if info.Description == "" {
				info.Description = m[2]
			}
		}
	}
	for _, m := range workflowPhaseRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{Title: m[1], Detail: m[2]})
	}
	for _, m := range workflowPhaseCallRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{Title: m[1]})
	}
	seenKeys := map[string]bool{}
	for _, m := range workflowLensKeyRE.FindAllStringSubmatch(script, -1) {
		key := m[1]
		if key == "" || seenKeys[key] {
			continue
		}
		seenKeys[key] = true
		info.LensKeys = append(info.LensKeys, key)
	}
	for _, m := range workflowLabelKeyRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[1], m[2], m[0])
	}
	for _, m := range workflowPhaseKeyRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[2], m[1], m[0])
	}
	for _, m := range workflowLabelLiteralRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[1], m[2], m[0])
	}
	for _, m := range workflowPhaseLiteralRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[2], m[1], m[0])
	}
	return info
}

func addWorkflowPhase(info *workflowScriptInfo, phase cass.WorkflowPhase) {
	if info == nil || phase.Title == "" {
		return
	}
	for _, p := range info.Phases {
		if p.Title == phase.Title {
			return
		}
	}
	info.Phases = append(info.Phases, phase)
}

func addWorkflowAgentSpecs(info *workflowScriptInfo, label, phase, options string) {
	if info == nil || label == "" || phase == "" {
		return
	}
	agentType := workflowAgentType(options)
	if strings.Contains(label, "${l.key}") {
		format := strings.ReplaceAll(label, "${l.key}", "%s")
		for _, key := range info.LensKeys {
			info.AgentSpecs = append(info.AgentSpecs, workflowAgentSpec{
				Label:     strings.ReplaceAll(format, "%s", key),
				Phase:     phase,
				AgentType: agentType,
			})
		}
		return
	}
	info.AgentSpecs = append(info.AgentSpecs, workflowAgentSpec{Label: label, Phase: phase, AgentType: agentType})
}

func workflowAgentType(optionsTail string) string {
	if m := workflowAgentTypeRE.FindStringSubmatch(optionsTail); len(m) == 2 {
		return m[1]
	}
	return ""
}

// readWorkflowAgents reads per-agent metadata for the fan-out transcripts in a
// workflow run's transcript dir (agent-*.jsonl, excluding acompact-* compaction
// helpers). Each agent's text is folded into the parent session's content
// elsewhere; here we capture just the metadata for tree rendering and per-agent
// search attribution. Journal start order is preferred; file-name order is the
// fallback for transcripts missing journal entries.
func readWorkflowAgents(w cass.WorkflowRun, script workflowScriptInfo) []cass.WorkflowAgent {
	dir := w.TranscriptDir
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	order, statuses := readWorkflowJournal(filepath.Join(dir, "journal.jsonl"))
	files := map[string]string{}
	var ids []string
	for _, fi := range infos {
		name := fi.Name()
		if fi.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-acompact") {
			continue
		}
		path := filepath.Join(dir, name)
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		files[id] = path
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := map[string]bool{}
	var ordered []string
	for _, id := range order {
		if files[id] == "" || seen[id] {
			continue
		}
		seen[id] = true
		ordered = append(ordered, id)
	}
	for _, id := range ids {
		if !seen[id] {
			ordered = append(ordered, id)
		}
	}
	var agents []cass.WorkflowAgent
	for i, id := range ordered {
		path := files[id]
		a := cass.WorkflowAgent{ID: id, SourcePath: path, Title: id}

		entries, err := cc.ReadFile(context.Background(), path)
		if err == nil && len(entries) > 0 {
			if t := workflowAgentTitle(cc.Summarize(path, entries)); t != "" {
				a.Title = t
			}
			for _, e := range entries {
				if e.Message == nil {
					continue
				}
				a.ToolCalls += len(e.Message.ToolUses())
				if e.Message.Usage != nil {
					a.Tokens += e.Message.Usage.InputTokens + e.Message.Usage.OutputTokens
				}
			}
			a.Status = firstNonEmpty(statuses[id], "completed")
		}
		if a.Status == "" {
			a.Status = statuses[id]
		}
		if a.Status == "" {
			a.Status = "unknown"
		}
		if a.Status == "running" && workflowCompleted(w) {
			a.Status = "completed"
		}
		if meta := readWorkflowAgentMeta(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); meta.AgentType != "" {
			a.AgentType = meta.AgentType
		}
		if spec := inferWorkflowAgentSpec(entries, script, i); spec.Label != "" || spec.Phase != "" || spec.AgentType != "" {
			a.Label = spec.Label
			a.Phase = spec.Phase
			if a.AgentType == "" {
				a.AgentType = spec.AgentType
			}
		}
		agents = append(agents, a)
	}
	return agents
}

func workflowCompleted(w cass.WorkflowRun) bool {
	return !w.CompletedAt.IsZero() || strings.EqualFold(w.Status, "completed")
}

func readWorkflowJournal(path string) ([]string, map[string]string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var order []string
	statuses := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			AgentID string `json:"agentId"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.AgentID == "" {
			continue
		}
		switch ev.Type {
		case "started":
			order = append(order, ev.AgentID)
			if statuses[ev.AgentID] == "" {
				statuses[ev.AgentID] = "running"
			}
		case "result":
			statuses[ev.AgentID] = "completed"
		case "error", "failed":
			statuses[ev.AgentID] = "error"
		}
	}
	return order, statuses
}

type workflowAgentMeta struct {
	AgentType string `json:"agentType"`
}

func readWorkflowAgentMeta(path string) workflowAgentMeta {
	var meta workflowAgentMeta
	b, err := os.ReadFile(path)
	if err != nil {
		return meta
	}
	_ = json.Unmarshal(b, &meta)
	return meta
}

func inferWorkflowAgentSpec(entries []cc.Entry, script workflowScriptInfo, index int) workflowAgentSpec {
	prompt := workflowAgentPrompt(entries)
	if fromPrompt := workflowAgentSpecFromPrompt(prompt, script.LensKeys); fromPrompt.Label != "" || fromPrompt.Phase != "" {
		if fromScript := workflowAgentSpecByName(script.AgentSpecs, fromPrompt); fromScript.AgentType != "" {
			fromPrompt.AgentType = fromScript.AgentType
		}
		return fromPrompt
	}
	if index >= 0 && index < len(script.AgentSpecs) {
		return script.AgentSpecs[index]
	}
	return workflowAgentSpec{}
}

func workflowAgentSpecByName(specs []workflowAgentSpec, want workflowAgentSpec) workflowAgentSpec {
	for _, spec := range specs {
		if spec.Label == want.Label && spec.Phase == want.Phase {
			return spec
		}
	}
	return workflowAgentSpec{}
}

func workflowAgentPrompt(entries []cc.Entry) string {
	for _, e := range entries {
		if e.Message == nil || e.Message.Role != "user" || e.Message.IsToolResultOnly() || e.IsMeta {
			continue
		}
		if text := strings.TrimSpace(e.Message.TextContent()); text != "" {
			return text
		}
	}
	return ""
}

func workflowAgentSpecFromPrompt(prompt string, lensKeys []string) workflowAgentSpec {
	if prompt == "" {
		return workflowAgentSpec{}
	}
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "you are the synthesizer") {
		return workflowAgentSpec{Label: "synthesize", Phase: "Synthesize"}
	}
	if m := workflowStressLensRE.FindStringSubmatch(prompt); len(m) == 2 {
		key := strings.TrimSpace(m[1])
		switch {
		case strings.Contains(prompt, "STRONGEST IDEA"):
			return workflowAgentSpec{Label: "stress-strong:" + key, Phase: "Stress"}
		case strings.Contains(prompt, "WEAKEST ASSUMPTION"):
			return workflowAgentSpec{Label: "stress-weak:" + key, Phase: "Stress"}
		}
	}
	if m := workflowLensTitleRE.FindStringSubmatch(prompt); len(m) == 2 {
		title := strings.ToLower(m[1])
		for _, key := range lensKeys {
			if lensTitleMatchesKey(title, key) {
				return workflowAgentSpec{Label: "lens:" + key, Phase: "Lenses"}
			}
		}
	}
	return workflowAgentSpec{}
}

func lensTitleMatchesKey(title, key string) bool {
	parts := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r < 'a' || r > 'z'
	})
	for _, p := range parts {
		if len(p) <= 2 {
			continue
		}
		if strings.Contains(title, p) {
			return true
		}
	}
	return false
}

// workflowAgentTitle derives a short title from a summary, matching
// titleFromSummary's 80-char truncation.
func workflowAgentTitle(s cc.SessionSummary) string {
	t := s.CustomTitle
	if t == "" {
		t = s.FirstPrompt
	}
	if len(t) > 80 {
		t = t[:80] + "..."
	}
	return t
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
