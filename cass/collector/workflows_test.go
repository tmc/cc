package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	agents := readWorkflowAgents(cass.WorkflowRun{TranscriptDir: dir}, info)
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

	agents := readWorkflowAgents(cass.WorkflowRun{
		TranscriptDir: dir,
		CompletedAt:   time.Now(),
	}, workflowScriptInfo{LensKeys: []string{"architecture"}})
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

func writeWorkflowAgentTestFile(t *testing.T, path, prompt string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":        "user",
		"isSidechain": true,
		"agentId":     strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent-"), ".jsonl"),
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
