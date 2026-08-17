package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

func TestExtractWorkflowsFromClaudeSession(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "-work-repo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(projectDir, "sid.jsonl")
	sessionDir := filepath.Join(projectDir, "sid")
	transcriptDir := filepath.Join(sessionDir, "subagents", "workflows", "wf_abc")
	if err := os.MkdirAll(filepath.Join(sessionDir, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := []string{
		`{"type":"attachment","attachment":{"type":"workflow_keyword_request"},"timestamp":"2026-05-28T17:14:00Z"}`,
		`{"type":"assistant","timestamp":"2026-05-28T17:15:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Workflow","input":{"script":"export const meta = { name: 'perf-dive', description: 'measure and verify' }\nphase('Measure')"}}]}}`,
		`{"type":"user","timestamp":"2026-05-28T17:15:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Workflow launched in background. Task ID: t1\nSummary: measure and verify\nTranscript dir: ` + transcriptDir + `\nScript file: ` + filepath.Join(sessionDir, "workflows", "scripts", "perf.js") + `\nRun ID: wf_abc","is_error":false}]},"toolUseResult":{"status":"async_launched","taskId":"t1","runId":"wf_abc","summary":"measure and verify","transcriptDir":"` + transcriptDir + `","scriptPath":"` + filepath.Join(sessionDir, "workflows", "scripts", "perf.js") + `"}}`,
	}
	if err := os.WriteFile(sessionPath, []byte(lines[0]+"\n"+lines[1]+"\n"+lines[2]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := `{"runId":"wf_abc","taskId":"t1","timestamp":"2026-05-28T17:20:00Z","script":"export const meta = { name: 'perf-dive', description: 'measure and verify' }","scriptPath":"` + filepath.Join(sessionDir, "workflows", "scripts", "perf.js") + `","result":{"ok":true}}`
	if err := os.WriteFile(filepath.Join(sessionDir, "workflows", "wf_abc.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-a.jsonl", "agent-b.jsonl"} {
		if err := os.WriteFile(filepath.Join(transcriptDir, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "journal.jsonl"), []byte("{}\n{}\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := (&ClaudeCode{}).parseSession(context.Background(), cass.ScanConfig{}, sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Workflows) != 1 {
		t.Fatalf("workflows = %d, want 1", len(sess.Workflows))
	}
	w := sess.Workflows[0]
	if w.RunID != "wf_abc" || w.TaskID != "t1" || w.Name != "perf-dive" || w.Status != "completed" {
		t.Fatalf("workflow = %+v", w)
	}
	if len(w.Phases) != 1 || w.Phases[0].Title != "Measure" {
		t.Fatalf("phases = %+v, want Measure from phase() call", w.Phases)
	}
	if w.AgentCount != 2 || w.JournalEventCount != 3 {
		t.Fatalf("workflow counts = agents %d journal %d", w.AgentCount, w.JournalEventCount)
	}
	if sess.Stats.WorkflowRuns != 1 || sess.Stats.WorkflowAsyncRuns != 1 || sess.Stats.WorkflowAgentRuns != 2 {
		t.Fatalf("workflow stats = %+v", sess.Stats)
	}
}

func TestReadWorkflowAgentsUsesScriptPhasesAndPromptAliases(t *testing.T) {
	dir := t.TempDir()
	script := `
export const meta = {
  name: 'mlxc-codegen-review',
  description: 'Independent review',
  phases: [
    { title: 'Lenses', detail: 'independent analytical perspectives' },
    { detail: 'adversarial verification', title: 'Stress' },
    { title: 'Synthesize' },
  ],
}
const LENSES = [
  { key: 'architecture', prompt: 'LENS: ARCHITECTURE. Decide.' },
  { key: 'custom-jaccl', prompt: 'LENS: CUSTOM/HANDWRITTEN APIs (esp. JACCL).' },
]
phase('Lenses')
agent(l.prompt, { label: ` + "`" + `lens:${l.key}` + "`" + `, phase: 'Lenses', schema: LENS_SCHEMA, agentType: 'Explore' })
agent('stress', { phase: 'Stress', schema: VERDICT_SCHEMA, agentType: 'Explore', label: ` + "`" + `stress-strong:${l.key}` + "`" + ` })
agent('stress', { phase: 'Stress', label: ` + "`" + `stress-weak:${l.key}` + "`" + `, schema: VERDICT_SCHEMA, agentType: 'Explore' })
phase('Synthesize')
agent('synth', { phase: 'Synthesize', label: 'synthesize' })
`
	info := workflowScriptInfoFromScript(script)
	if len(info.Phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(info.Phases))
	}
	if info.Phases[1].Title != "Stress" || info.Phases[1].Detail != "adversarial verification" || info.Phases[2].Title != "Synthesize" {
		t.Fatalf("phases = %+v, want reversed-detail and title-only phases", info.Phases)
	}
	if len(info.AgentSpecs) != 7 {
		t.Fatalf("agent specs = %d, want 7", len(info.AgentSpecs))
	}
	if info.AgentSpecs[0].AgentType != "Explore" || info.AgentSpecs[2].AgentType != "Explore" {
		t.Fatalf("agent types = %q, %q, want Explore",
			info.AgentSpecs[0].AgentType, info.AgentSpecs[2].AgentType)
	}

	writeWorkflowAgentTestFile(t, filepath.Join(dir, "agent-b.jsonl"),
		`ADVERSARIAL STRESS TEST. A reviewer using the "architecture" lens concluded:

STRONGEST IDEA: lock the API first`)
	writeWorkflowAgentTestFile(t, filepath.Join(dir, "agent-a.jsonl"),
		`You are reviewing.

LENS: CUSTOM/HANDWRITTEN APIs (esp. JACCL). Decide.`)
	writeWorkflowAgentTestFile(t, filepath.Join(dir, "agent-c.jsonl"),
		`You are the SYNTHESIZER. Below are 5 analytical lens views.`)
	if err := os.WriteFile(filepath.Join(dir, "agent-a.meta.json"), []byte(`{"agentType":"Explore"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := "" +
		`{"type":"started","agentId":"b"}` + "\n" +
		`{"type":"started","agentId":"a"}` + "\n" +
		`{"type":"started","agentId":"c"}` + "\n" +
		`{"type":"result","agentId":"a"}` + "\n" +
		`{"type":"result","agentId":"c"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, _ := readWorkflowAgents(cass.WorkflowRun{TranscriptDir: dir}, info, nil)
	if len(agents) != 3 {
		t.Fatalf("agents = %d, want 3", len(agents))
	}
	tests := []struct {
		index     int
		id        string
		label     string
		phase     string
		status    string
		agentType string
	}{
		{0, "b", "stress-strong:architecture", "Stress", "running", "Explore"},
		{1, "a", "lens:custom-jaccl", "Lenses", "completed", "Explore"},
		{2, "c", "synthesize", "Synthesize", "completed", ""},
	}
	for _, tt := range tests {
		a := agents[tt.index]
		if a.ID != tt.id || a.Label != tt.label || a.Phase != tt.phase || a.Status != tt.status || a.AgentType != tt.agentType {
			t.Fatalf("agent %d = %+v, want id=%s label=%s phase=%s status=%s type=%s",
				tt.index, a, tt.id, tt.label, tt.phase, tt.status, tt.agentType)
		}
	}
}

func TestReadWorkflowAgentsClosesStartedAgentsWhenWorkflowCompletes(t *testing.T) {
	dir := t.TempDir()
	writeWorkflowAgentTestFile(t, filepath.Join(dir, "agent-a.jsonl"),
		`LENS: ARCHITECTURE. Decide.`)
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"type":"started","agentId":"a"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, _ := readWorkflowAgents(cass.WorkflowRun{
		TranscriptDir: dir,
		CompletedAt:   time.Now(),
	}, workflowScriptInfo{LensKeys: []string{"architecture"}}, nil)
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	if agents[0].Status != "completed" {
		t.Fatalf("status = %q, want completed", agents[0].Status)
	}
}

func TestWorkflowScriptInfoAcceptsBacktickStrings(t *testing.T) {
	script := "export const meta = { name: `backtick-flow`, description: `uses template-string literals` }\n" +
		"export const meta2 = { phases: [\n" +
		"  { title: `Build`, detail: `prepare inputs` },\n" +
		"  { detail: `write summary`, title: `Synthesize` },\n" +
		"] }\n" +
		"const LENSES = [{ key: `api` }]\n" +
		"phase(`Review`)\n" +
		"agent('review', { label: `lens:${l.key}`, phase: `Review`, agentType: `Explore` })\n" +
		"agent('final', { phase: `Synthesize`, label: `synthesize`, agentType: `Writer` })\n"

	info := workflowScriptInfoFromScript(script)
	if info.Name != "backtick-flow" || info.Description != "uses template-string literals" {
		t.Fatalf("meta = %q/%q, want backtick-flow/uses template-string literals", info.Name, info.Description)
	}
	if len(info.Phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(info.Phases))
	}
	if info.Phases[0].Title != "Build" || info.Phases[0].Detail != "prepare inputs" ||
		info.Phases[1].Title != "Synthesize" || info.Phases[1].Detail != "write summary" ||
		info.Phases[2].Title != "Review" {
		t.Fatalf("phases = %+v, want Build, Synthesize, Review", info.Phases)
	}
	if len(info.AgentSpecs) != 2 {
		t.Fatalf("agent specs = %d, want 2 without duplicate template-label matches", len(info.AgentSpecs))
	}
	if info.AgentSpecs[0] != (workflowAgentSpec{Label: "lens:api", Phase: "Review", AgentType: "Explore"}) {
		t.Fatalf("first agent spec = %+v, want lens:api Review Explore", info.AgentSpecs[0])
	}
	if info.AgentSpecs[1] != (workflowAgentSpec{Label: "synthesize", Phase: "Synthesize", AgentType: "Writer"}) {
		t.Fatalf("second agent spec = %+v, want synthesize Synthesize Writer", info.AgentSpecs[1])
	}
}

func TestWorkflowAgentTitleUsesGeneratedTitleCleanup(t *testing.T) {
	tests := []struct {
		name string
		sum  cc.SessionSummary
		want string
	}{
		{
			name: "goal objective",
			sum: cc.SessionSummary{
				FirstPrompt: "<goal_context><objective>review the branch</objective></goal_context>",
			},
			want: "goal: review the branch",
		},
		{
			name: "image payload",
			sum: cc.SessionSummary{
				FirstPrompt: "<image name=[Image #1]> input_image data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAA",
			},
			want: "image input",
		},
		{
			name: "custom title",
			sum: cc.SessionSummary{
				CustomTitle: "named agent",
				FirstPrompt: "ignored prompt",
			},
			want: "named agent",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workflowAgentTitle(tt.sum); got != tt.want {
				t.Fatalf("workflowAgentTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func writeWorkflowAgentTestFile(t *testing.T, path, prompt string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":        "user",
		"isSidechain": true,
		"agentId":     strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent-"), ".jsonl"),
		"timestamp":   "2026-08-17T08:00:00Z",
		"message": map[string]any{
			"role":    "user",
			"content": prompt,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExtractWorkflowsFullStateFile covers the full shape a real run writes:
// the state file's own totals and workflowProgress records, the journal's
// resume keys and structured results, meta.json's spawn depth and worktree,
// and a second run that is still in flight and so has no state file at all.
func TestExtractWorkflowsFullStateFile(t *testing.T) {
	projectDir := t.TempDir()
	sessionPath := filepath.Join(projectDir, "sid.jsonl")
	sessionDir := filepath.Join(projectDir, "sid")
	doneDir := filepath.Join(sessionDir, "subagents", "workflows", "wf_done")
	liveDir := filepath.Join(sessionDir, "subagents", "workflows", "wf_live")
	for _, d := range []string{filepath.Join(sessionDir, "workflows"), doneDir, liveDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	state := `{
		"runId": "wf_done", "taskId": "t1", "workflowName": "close-defects",
		"summary": "fix the regressions", "status": "completed",
		"timestamp": "2026-08-17T05:10:10.034Z", "startTime": 1786940971980,
		"durationMs": 2438054, "agentCount": 2, "totalTokens": 437741, "totalToolCalls": 182,
		"defaultModel": "claude-opus-5[1m]",
		"logs": ["ground: gen=f88a756", "typedegrade: not fixing"],
		"phases": [{"title": "Ground"}, {"title": "Diagnose", "detail": "trace each defect"}],
		"args": "{\"postregen\":{\"fixed\":0}}",
		"result": {"ok": true},
		"workflowProgress": [
			{"type": "workflow_phase", "index": 1, "title": "Ground"},
			{"type": "workflow_agent", "index": 1, "label": "ground", "phaseIndex": 1,
			 "phaseTitle": "Ground", "agentId": "aaa", "model": "claude-opus-5[1m]",
			 "state": "done", "attempt": 1, "tokens": 38964, "toolCalls": 3,
			 "durationMs": 16327, "lastToolName": "StructuredOutput",
			 "lastToolSummary": "f88a756", "promptPreview": "YOU ARE WORKING IN /repo",
			 "resultPreview": "{\"gen_head\":\"f88a756\"}"},
			{"type": "workflow_agent", "index": 2, "label": "diag:typedegrade", "phaseIndex": 2,
			 "phaseTitle": "Diagnose", "agentId": "bbb", "model": "claude-opus-5[1m]",
			 "state": "done", "attempt": 1, "isolation": "worktree",
			 "tokens": 103581, "toolCalls": 56, "durationMs": 487933}
		]
	}`
	if err := os.WriteFile(filepath.Join(sessionDir, "workflows", "wf_done.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	writeWorkflowAgentTestFile(t, filepath.Join(doneDir, "agent-aaa.jsonl"), "ground the run")
	writeWorkflowAgentTestFile(t, filepath.Join(doneDir, "agent-bbb.jsonl"), "diagnose typedegrade")
	if err := os.WriteFile(filepath.Join(doneDir, "agent-bbb.meta.json"),
		[]byte(`{"agentType":"workflow-subagent","worktreePath":"/repo/.claude/worktrees/wf_done-2","spawnDepth":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	journal := `{"type":"started","key":"v2:aaakey","agentId":"aaa"}` + "\n" +
		`{"type":"result","key":"v2:aaakey","agentId":"aaa","result":{"gen_head":"f88a756"}}` + "\n" +
		`{"type":"started","key":"v2:bbbkey","agentId":"bbb"}` + "\n"
	if err := os.WriteFile(filepath.Join(doneDir, "journal.jsonl"), []byte(journal), 0o644); err != nil {
		t.Fatal(err)
	}

	// The state-file-less run: a transcript dir, a journal, and a saved script.
	if err := os.MkdirAll(filepath.Join(sessionDir, "workflows", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "workflows", "scripts", "block-rules-wf_live.js"),
		[]byte("export const meta = { name: 'block-rules' }\nphase('Scan')"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeWorkflowAgentTestFile(t, filepath.Join(liveDir, "agent-ccc.jsonl"), "still working")
	if err := os.WriteFile(filepath.Join(liveDir, "journal.jsonl"),
		[]byte(`{"type":"started","key":"v2:ccckey","agentId":"ccc"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runs := ExtractWorkflows(sessionPath, nil)
	byID := map[string]cass.WorkflowRun{}
	for _, r := range runs {
		byID[r.RunID] = r
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2 (%v)", len(runs), byID)
	}

	w := byID["wf_done"]
	if w.Name != "close-defects" || w.Status != "completed" || w.Summary != "fix the regressions" {
		t.Fatalf("run = %+v, want state-file name/status/summary", w)
	}
	if w.Model != "claude-opus-5[1m]" || w.DurationMs != 2438054 || w.TotalTokens != 437741 || w.TotalToolCalls != 182 {
		t.Fatalf("run totals = %q %d %d %d", w.Model, w.DurationMs, w.TotalTokens, w.TotalToolCalls)
	}
	if want := time.UnixMilli(1786940971980); !w.StartedAt.Equal(want) {
		t.Fatalf("started = %v, want %v", w.StartedAt, want)
	}
	if len(w.Logs) != 2 || w.Logs[0] != "ground: gen=f88a756" {
		t.Fatalf("logs = %q", w.Logs)
	}
	if len(w.Phases) != 2 || w.Phases[1].Detail != "trace each defect" {
		t.Fatalf("phases = %+v, want the state file's phases", w.Phases)
	}
	if w.Args != `{"postregen":{"fixed":0}}` {
		t.Fatalf("args = %q, want the unwrapped payload", w.Args)
	}
	if w.Result != `{"ok": true}` {
		t.Fatalf("result = %q", w.Result)
	}

	if len(w.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(w.Agents))
	}
	a := w.Agents[0]
	if a.ID != "aaa" || a.Label != "ground" || a.Phase != "Ground" || a.Index != 1 || a.PhaseIndex != 1 {
		t.Fatalf("agent[0] = %+v, want the progress record's label/phase/ordinals", a)
	}
	if a.Model != "claude-opus-5[1m]" || a.Attempt != 1 || a.Tokens != 38964 || a.ToolCalls != 3 || a.DurationMs != 16327 {
		t.Fatalf("agent[0] metrics = %+v, want the progress record's", a)
	}
	if a.LastToolName != "StructuredOutput" || a.LastToolSummary != "f88a756" ||
		a.PromptPreview != "YOU ARE WORKING IN /repo" || a.ResultPreview != `{"gen_head":"f88a756"}` {
		t.Fatalf("agent[0] previews = %+v", a)
	}
	if a.CacheKey != "v2:aaakey" || a.Result != `{"gen_head":"f88a756"}` || a.Status != "completed" {
		t.Fatalf("agent[0] journal fields = %+v", a)
	}
	b := w.Agents[1]
	if b.Label != "diag:typedegrade" || b.Isolation != "worktree" || b.SpawnDepth != 1 ||
		b.WorktreePath != "/repo/.claude/worktrees/wf_done-2" || b.AgentType != "workflow-subagent" {
		t.Fatalf("agent[1] = %+v, want progress isolation and meta.json fields", b)
	}
	// The journal never recorded a result for bbb, but the progress record's
	// terminal state did.
	if b.Status != "completed" {
		t.Fatalf("agent[1] status = %q, want completed from progress state", b.Status)
	}

	live := byID["wf_live"]
	if live.Status != "unknown" || len(live.Agents) != 1 || live.Agents[0].CacheKey != "v2:ccckey" {
		t.Fatalf("in-flight run = %+v", live)
	}
	// The journal recorded a start and no result, and the run itself has not
	// completed, so the agent is still running.
	if live.Agents[0].Status != "running" {
		t.Fatalf("in-flight agent status = %q, want running", live.Agents[0].Status)
	}
	if live.StartedAt.IsZero() {
		t.Fatal("state-file-less run has no start time; want the first agent entry's timestamp")
	}
	if live.Name != "block-rules" || len(live.Phases) != 1 || live.Phases[0].Title != "Scan" {
		t.Fatalf("state-file-less run = name %q phases %+v, want them recovered from the saved script",
			live.Name, live.Phases)
	}
}
