package collector

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
	if w.AgentCount != 2 || w.JournalEventCount != 3 {
		t.Fatalf("workflow counts = agents %d journal %d", w.AgentCount, w.JournalEventCount)
	}
	if sess.Stats.WorkflowRuns != 1 || sess.Stats.WorkflowAsyncRuns != 1 || sess.Stats.WorkflowAgentRuns != 2 {
		t.Fatalf("workflow stats = %+v", sess.Stats)
	}
}
