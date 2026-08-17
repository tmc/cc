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

const workflowStringPattern = "(?:'([^']*)'|\"([^\"]*)\"|`([^`]*)`)"

var (
	workflowMetaRE         = regexp.MustCompile("(?m)\\b(name|description)\\s*:\\s*" + workflowStringPattern)
	workflowPhaseRE        = regexp.MustCompile("(?s)\\{\\s*title\\s*:\\s*" + workflowStringPattern + "\\s*,\\s*detail\\s*:\\s*" + workflowStringPattern + "\\s*\\}")
	workflowPhaseRevRE     = regexp.MustCompile("(?s)\\{\\s*detail\\s*:\\s*" + workflowStringPattern + "\\s*,\\s*title\\s*:\\s*" + workflowStringPattern + "\\s*\\}")
	workflowPhaseTitleRE   = regexp.MustCompile("(?s)\\{\\s*title\\s*:\\s*" + workflowStringPattern + "\\s*,?\\s*\\}")
	workflowPhaseCallRE    = regexp.MustCompile("\\bphase\\(\\s*" + workflowStringPattern + "\\s*\\)")
	workflowLensKeyRE      = regexp.MustCompile("\\bkey\\s*:\\s*" + workflowStringPattern)
	workflowLabelKeyRE     = regexp.MustCompile("label\\s*:\\s*`([^`]*\\$\\{l\\.key\\}[^`]*)`\\s*,\\s*phase\\s*:\\s*" + workflowStringPattern + "([^}]*)")
	workflowPhaseKeyRE     = regexp.MustCompile("phase\\s*:\\s*" + workflowStringPattern + "[^}]*label\\s*:\\s*`([^`]*\\$\\{l\\.key\\}[^`]*)`([^}]*)")
	workflowLabelLiteralRE = regexp.MustCompile("label\\s*:\\s*" + workflowStringPattern + "\\s*,\\s*phase\\s*:\\s*" + workflowStringPattern + "([^}]*)")
	workflowPhaseLiteralRE = regexp.MustCompile("phase\\s*:\\s*" + workflowStringPattern + "[^}]*label\\s*:\\s*" + workflowStringPattern + "([^}]*)")
	workflowAgentTypeRE    = regexp.MustCompile("agentType\\s*:\\s*" + workflowStringPattern)
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

	states := readWorkflowState(sessionPath, byRun)

	if len(byRun) == 0 {
		return nil
	}
	out := make([]cass.WorkflowRun, 0, len(byRun))
	for _, w := range byRun {
		script := enrichWorkflowFromScriptFile(w)
		if w.TranscriptDir != "" {
			var earliest time.Time
			w.Agents, earliest = readWorkflowAgents(*w, script, states[w.RunID].Progress)
			if len(w.Agents) > w.AgentCount {
				w.AgentCount = len(w.Agents)
			}
			// A run still in flight has no state file, so its only clock is the
			// first thing its agents said.
			if w.StartedAt.IsZero() {
				w.StartedAt = earliest
			}
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

// workflowStateFile is the on-disk state a completed or in-flight native
// workflow run writes to <session>/workflows/<run_id>.json. It is the
// authoritative record: the parent JSONL only sees the launch and the async
// hand-off, and the script source only says what was intended, not what ran.
type workflowStateFile struct {
	RunID          string                `json:"runId"`
	TaskID         string                `json:"taskId"`
	Timestamp      time.Time             `json:"timestamp"`
	StartTime      int64                 `json:"startTime"` // unix millis
	DurationMs     int64                 `json:"durationMs"`
	Script         string                `json:"script"`
	ScriptPath     string                `json:"scriptPath"`
	WorkflowName   string                `json:"workflowName"`
	Status         string                `json:"status"`
	Summary        string                `json:"summary"`
	DefaultModel   string                `json:"defaultModel"`
	AgentCount     int                   `json:"agentCount"`
	TotalTokens    int                   `json:"totalTokens"`
	TotalToolCalls int                   `json:"totalToolCalls"`
	Logs           []string              `json:"logs"`
	Phases         []cass.WorkflowPhase  `json:"phases"`
	Progress       []workflowProgressRec `json:"workflowProgress"`
	Args           json.RawMessage       `json:"args"`
	Result         json.RawMessage       `json:"result"`
}

// workflowProgressRec is one workflowProgress entry. Entries are either a
// phase marker (type "workflow_phase") or a per-agent record (type
// "workflow_agent"); the agent records are the harness's own account of each
// fan-out call and beat anything inferred from the script text.
type workflowProgressRec struct {
	Type            string `json:"type"`
	Index           int    `json:"index"`
	Title           string `json:"title"`
	Label           string `json:"label"`
	PhaseIndex      int    `json:"phaseIndex"`
	PhaseTitle      string `json:"phaseTitle"`
	AgentID         string `json:"agentId"`
	Model           string `json:"model"`
	State           string `json:"state"`
	Attempt         int    `json:"attempt"`
	Isolation       string `json:"isolation"`
	Tokens          int    `json:"tokens"`
	ToolCalls       int    `json:"toolCalls"`
	DurationMs      int64  `json:"durationMs"`
	LastToolName    string `json:"lastToolName"`
	LastToolSummary string `json:"lastToolSummary"`
	PromptPreview   string `json:"promptPreview"`
	ResultPreview   string `json:"resultPreview"`
}

// readWorkflowStateFile decodes one workflows/<run_id>.json. A zero RunID
// means the file was missing or not a workflow state file.
func readWorkflowStateFile(path string) workflowStateFile {
	var raw workflowStateFile
	if path == "" {
		return raw
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return workflowStateFile{}
	}
	if json.Unmarshal(b, &raw) != nil {
		return workflowStateFile{}
	}
	return raw
}

func readWorkflowState(sessionPath string, byRun map[string]*cass.WorkflowRun) map[string]workflowStateFile {
	base := strings.TrimSuffix(sessionPath, ".jsonl")
	states := map[string]workflowStateFile{}
	infos, err := os.ReadDir(filepath.Join(base, "workflows"))
	if err == nil {
		for _, fi := range infos {
			if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".json") {
				continue
			}
			path := filepath.Join(base, "workflows", fi.Name())
			raw := readWorkflowStateFile(path)
			if raw.RunID == "" {
				continue
			}
			states[raw.RunID] = raw
			applyWorkflowState(byRun, base, path, raw)
		}
	}
	// A run in flight (or one that never finished) has a transcript dir but no
	// state file; index it from the dir alone so it is not invisible. Its
	// status is unknown rather than "running": the state file is the only
	// record of completion, and its absence cannot distinguish the two.
	dirs, err := os.ReadDir(filepath.Join(base, "subagents", "workflows"))
	if err != nil {
		return states
	}
	scripts := workflowScriptsByRunID(filepath.Join(base, "workflows", "scripts"))
	for _, fi := range dirs {
		id := fi.Name()
		if !fi.IsDir() || !strings.HasPrefix(id, "wf_") || byRun[id] != nil {
			continue
		}
		w := &cass.WorkflowRun{
			RunID:         id,
			Status:        "unknown",
			TranscriptDir: filepath.Join(base, "subagents", "workflows", id),
		}
		if s, ok := scripts[id]; ok {
			w.ScriptPath = s.path
			w.Name = s.name
		}
		byRun[id] = w
	}
	return states
}

// workflowScript is a run's saved script file, whose name encodes the workflow
// name the run was launched under.
type workflowScript struct {
	path string
	name string
}

// workflowScriptsByRunID indexes workflows/scripts/<name>-<run_id>.js by run
// ID. For a run whose state file is missing, this is the only place its name
// and declared phases survive.
func workflowScriptsByRunID(dir string) map[string]workflowScript {
	out := map[string]workflowScript{}
	infos, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, fi := range infos {
		name := fi.Name()
		if fi.IsDir() || !strings.HasSuffix(name, ".js") {
			continue
		}
		stem := strings.TrimSuffix(name, ".js")
		i := strings.LastIndex(stem, "-wf_")
		if i < 0 {
			continue
		}
		out[stem[i+1:]] = workflowScript{
			path: filepath.Join(dir, name),
			name: stem[:i],
		}
	}
	return out
}

func applyWorkflowState(byRun map[string]*cass.WorkflowRun, base, path string, raw workflowStateFile) {
	w := byRun[raw.RunID]
	if w == nil {
		w = &cass.WorkflowRun{RunID: raw.RunID}
		byRun[raw.RunID] = w
	}
	w.TaskID = firstNonEmpty(w.TaskID, raw.TaskID)
	w.ScriptPath = firstNonEmpty(w.ScriptPath, raw.ScriptPath)
	w.Name = firstNonEmpty(w.Name, raw.WorkflowName)
	w.Summary = firstNonEmpty(w.Summary, raw.Summary)
	w.Model = firstNonEmpty(w.Model, raw.DefaultModel)
	if raw.StartTime > 0 {
		w.StartedAt = time.UnixMilli(raw.StartTime)
	} else if w.StartedAt.IsZero() {
		w.StartedAt = raw.Timestamp
	}
	if raw.DurationMs > 0 {
		w.DurationMs = raw.DurationMs
	}
	if raw.AgentCount > 0 {
		w.AgentCount = raw.AgentCount
	}
	w.TotalTokens = raw.TotalTokens
	w.TotalToolCalls = raw.TotalToolCalls
	w.Logs = raw.Logs
	w.Args = rawJSONString(raw.Args)
	w.Result = rawJSONString(raw.Result)
	// The state file's phases are what actually ran; the script regex is only a
	// fallback for runs whose state file predates the phases field.
	if len(raw.Phases) > 0 {
		w.Phases = raw.Phases
	}
	applyWorkflowScriptInfo(w, workflowScriptInfoFromScript(raw.Script))
	if raw.Status != "" {
		w.Status = raw.Status
	} else if w.Result != "" {
		w.Status = "completed"
	}
	if workflowCompleted(*w) {
		w.CompletedAt = raw.Timestamp
	}
	w.SourcePath = path
	if w.TranscriptDir == "" {
		w.TranscriptDir = filepath.Join(base, "subagents", "workflows", raw.RunID)
	}
}

// rawJSONString renders a raw JSON value as text, treating absent and null
// alike so callers can test for emptiness.
func rawJSONString(b json.RawMessage) string {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return ""
	}
	// The harness sometimes stores args as a JSON string holding JSON; unwrap
	// it so the payload is searchable rather than escaped.
	if strings.HasPrefix(s, `"`) {
		var unquoted string
		if json.Unmarshal(b, &unquoted) == nil {
			return unquoted
		}
	}
	return s
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

// EnrichWorkflowMetadata fills script-declared phases and workflow-agent labels
// from the run artifacts still on disk. It is best-effort and is useful when an
// existing index row predates richer workflow metadata extraction.
func EnrichWorkflowMetadata(w *cass.WorkflowRun) {
	if w == nil {
		return
	}
	info := enrichWorkflowFromScriptFile(w)
	if needsWorkflowAgentEnrichment(w) {
		if agents, _ := readWorkflowAgents(*w, info, readWorkflowStateFile(w.SourcePath).Progress); len(agents) > 0 {
			w.Agents = agents
			if w.AgentCount == 0 {
				w.AgentCount = len(agents)
			}
		}
	}
}

func needsWorkflowAgentEnrichment(w *cass.WorkflowRun) bool {
	if w == nil || w.TranscriptDir == "" {
		return false
	}
	if len(w.Agents) == 0 {
		return true
	}
	for _, a := range w.Agents {
		if a.Label == "" || a.Phase == "" {
			return true
		}
	}
	return false
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
				info.Name = workflowString(m[2:5])
			}
		case "description":
			if info.Description == "" {
				info.Description = workflowString(m[2:5])
			}
		}
	}
	for _, m := range workflowPhaseRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{
			Title:  workflowString(m[1:4]),
			Detail: workflowString(m[4:7]),
		})
	}
	for _, m := range workflowPhaseRevRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{
			Title:  workflowString(m[4:7]),
			Detail: workflowString(m[1:4]),
		})
	}
	for _, m := range workflowPhaseTitleRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{Title: workflowString(m[1:4])})
	}
	for _, m := range workflowPhaseCallRE.FindAllStringSubmatch(script, -1) {
		addWorkflowPhase(&info, cass.WorkflowPhase{Title: workflowString(m[1:4])})
	}
	seenKeys := map[string]bool{}
	for _, m := range workflowLensKeyRE.FindAllStringSubmatch(script, -1) {
		key := workflowString(m[1:4])
		if key == "" || seenKeys[key] {
			continue
		}
		seenKeys[key] = true
		info.LensKeys = append(info.LensKeys, key)
	}
	for _, m := range workflowLabelKeyRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[1], workflowString(m[2:5]), m[0])
	}
	for _, m := range workflowPhaseKeyRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, m[4], workflowString(m[1:4]), m[0])
	}
	for _, m := range workflowLabelLiteralRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, workflowString(m[1:4]), workflowString(m[4:7]), m[0])
	}
	for _, m := range workflowPhaseLiteralRE.FindAllStringSubmatch(script, -1) {
		addWorkflowAgentSpecs(&info, workflowString(m[4:7]), workflowString(m[1:4]), m[0])
	}
	return info
}

func workflowString(groups []string) string {
	for _, s := range groups {
		if s != "" {
			return s
		}
	}
	return ""
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
			appendWorkflowAgentSpec(info, workflowAgentSpec{
				Label:     strings.ReplaceAll(format, "%s", key),
				Phase:     phase,
				AgentType: agentType,
			})
		}
		return
	}
	appendWorkflowAgentSpec(info, workflowAgentSpec{Label: label, Phase: phase, AgentType: agentType})
}

func appendWorkflowAgentSpec(info *workflowScriptInfo, spec workflowAgentSpec) {
	for _, existing := range info.AgentSpecs {
		if existing == spec {
			return
		}
	}
	info.AgentSpecs = append(info.AgentSpecs, spec)
}

func workflowAgentType(optionsTail string) string {
	if m := workflowAgentTypeRE.FindStringSubmatch(optionsTail); len(m) == 4 {
		return workflowString(m[1:4])
	}
	return ""
}

// readWorkflowAgents reads per-agent metadata for the fan-out transcripts in a
// workflow run's transcript dir (agent-*.jsonl, excluding acompact-* compaction
// helpers). Each agent's text is folded into the parent session's content
// elsewhere; here we capture just the metadata for tree rendering and per-agent
// search attribution. Journal start order is preferred; file-name order is the
// fallback for transcripts missing journal entries.
func readWorkflowAgents(w cass.WorkflowRun, script workflowScriptInfo, progress []workflowProgressRec) ([]cass.WorkflowAgent, time.Time) {
	dir := w.TranscriptDir
	var earliest time.Time
	infos, err := os.ReadDir(dir)
	if err != nil {
		return nil, earliest
	}
	order, journal := readWorkflowJournal(filepath.Join(dir, "journal.jsonl"))
	byAgent := map[string]workflowProgressRec{}
	for _, p := range progress {
		if p.Type == "workflow_agent" && p.AgentID != "" {
			byAgent[p.AgentID] = p
		}
	}
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
		ev := journal[id]

		entries, err := cc.ReadFile(context.Background(), path)
		if err == nil && len(entries) > 0 {
			if t := workflowAgentTitle(cc.Summarize(path, entries)); t != "" {
				a.Title = t
			}
			if ts := entries[0].Timestamp; !ts.IsZero() && (earliest.IsZero() || ts.Before(earliest)) {
				earliest = ts
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
			a.Status = firstNonEmpty(ev.Status, "completed")
		}
		if a.Status == "" {
			a.Status = ev.Status
		}
		if a.Status == "" {
			a.Status = "unknown"
		}
		if a.Status == "running" && workflowCompleted(w) {
			a.Status = "completed"
		}
		a.CacheKey = ev.Key
		a.Result = ev.Result
		meta := readWorkflowAgentMeta(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
		a.AgentType = meta.AgentType
		a.SpawnDepth = meta.SpawnDepth
		a.WorktreePath = meta.WorktreePath
		if spec := inferWorkflowAgentSpec(entries, script, i); spec.Label != "" || spec.Phase != "" || spec.AgentType != "" {
			a.Label = spec.Label
			a.Phase = spec.Phase
			if a.AgentType == "" {
				a.AgentType = spec.AgentType
			}
		}
		applyWorkflowProgress(&a, byAgent[id])
		agents = append(agents, a)
	}
	return agents, earliest
}

// applyWorkflowProgress overlays the harness's own record of an agent call. It
// wins over script-text inference for label and phase, and over the JSONL
// streaming-start snapshot for tokens and tool calls, because it is what the
// run actually reported.
func applyWorkflowProgress(a *cass.WorkflowAgent, p workflowProgressRec) {
	if p.AgentID == "" {
		return
	}
	if p.Label != "" {
		a.Label = p.Label
	}
	if p.PhaseTitle != "" {
		a.Phase = p.PhaseTitle
	}
	a.Index = p.Index
	a.PhaseIndex = p.PhaseIndex
	a.Model = p.Model
	a.Attempt = p.Attempt
	a.Isolation = p.Isolation
	a.DurationMs = p.DurationMs
	a.LastToolName = p.LastToolName
	a.LastToolSummary = p.LastToolSummary
	a.PromptPreview = p.PromptPreview
	a.ResultPreview = p.ResultPreview
	if p.Tokens > 0 {
		a.Tokens = p.Tokens
	}
	if p.ToolCalls > 0 {
		a.ToolCalls = p.ToolCalls
	}
	if s := workflowAgentState(p.State); s != "" {
		a.Status = s
	}
}

// workflowAgentState maps a progress record's state onto the status vocabulary
// the journal uses.
func workflowAgentState(state string) string {
	switch state {
	case "done":
		return "completed"
	case "error", "failed":
		return "error"
	case "":
		return ""
	default:
		return state
	}
}

func workflowCompleted(w cass.WorkflowRun) bool {
	return !w.CompletedAt.IsZero() || strings.EqualFold(w.Status, "completed")
}

// workflowJournalAgent is what journal.jsonl records about one fan-out agent:
// its resume cache key, its terminal status, and the value it returned.
type workflowJournalAgent struct {
	Key    string
	Status string
	Result string
}

// readWorkflowJournal reads a run's journal.jsonl, returning agent start order
// and per-agent journal state. The journal is the only place a resumed run's
// cache key and an agent's structured return value are recorded.
func readWorkflowJournal(path string) ([]string, map[string]workflowJournalAgent) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	var order []string
	agents := map[string]workflowJournalAgent{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), cc.MaxLineSize)
	for sc.Scan() {
		var ev struct {
			Type    string          `json:"type"`
			AgentID string          `json:"agentId"`
			Key     string          `json:"key"`
			Result  json.RawMessage `json:"result"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.AgentID == "" {
			continue
		}
		a := agents[ev.AgentID]
		if ev.Key != "" {
			a.Key = ev.Key
		}
		switch ev.Type {
		case "started":
			order = append(order, ev.AgentID)
			if a.Status == "" {
				a.Status = "running"
			}
		case "result":
			a.Status = "completed"
			if s := rawJSONString(ev.Result); s != "" {
				a.Result = s
			}
		case "error", "failed":
			a.Status = "error"
		}
		agents[ev.AgentID] = a
	}
	return order, agents
}

type workflowAgentMeta struct {
	AgentType    string `json:"agentType"`
	SpawnDepth   int    `json:"spawnDepth"`
	WorktreePath string `json:"worktreePath"`
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
		t = cleanGeneratedTitle(s.FirstPrompt)
	}
	return truncateTitle(t)
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
