package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tmc/cc/cass"
)

func TestCodexDetectAndScan(t *testing.T) {
	tmp := t.TempDir()
	codexHome := filepath.Join(tmp, ".codex")
	sessionsDir := filepath.Join(codexHome, "sessions", "2026", "02", "25")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	t.Setenv("CODEX_HOME", codexHome)

	cliPath := filepath.Join(sessionsDir, "rollout-2026-02-25T10-00-00-cli.jsonl")
	appPath := filepath.Join(sessionsDir, "rollout-2026-02-25T11-00-00-app.jsonl")

	writeCollectorJSONL(t, cliPath,
		map[string]any{
			"timestamp": "2026-02-25T10:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "codex-cli-session",
				"cwd":         "/work/cli",
				"originator":  "codex_cli_rs",
				"source":      "cli",
				"cli_version": "0.58.0",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T10:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "cli prompt"},
				},
			},
		},
	)
	writeCollectorJSONL(t, appPath,
		map[string]any{
			"timestamp": "2026-02-25T11:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":          "codex-app-session",
				"cwd":         "/work/app",
				"originator":  "Codex Desktop",
				"source":      "vscode",
				"cli_version": "0.104.0-alpha.1",
			},
		},
		map[string]any{
			"timestamp": "2026-02-25T11:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "app prompt"},
				},
			},
		},
	)

	c := &Codex{}
	det, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !det.Found {
		t.Fatalf("Detect.Found = false, want true")
	}
	if len(det.Paths) != 1 || det.Paths[0] != filepath.Join(codexHome, "sessions") {
		t.Fatalf("Detect.Paths = %#v", det.Paths)
	}

	ch := make(chan cass.Session, 8)
	go func() {
		_ = c.Scan(context.Background(), cass.ScanConfig{}, ch)
	}()

	var got []cass.Session
	for s := range ch {
		got = append(got, s)
	}
	if len(got) != 2 {
		t.Fatalf("scan sessions = %d, want 2", len(got))
	}

	byAgent := make(map[string]cass.Session)
	for _, s := range got {
		byAgent[s.Agent] = s
	}

	cli := byAgent["codex-cli"]
	if cli.ID != "codex-cli-session" {
		t.Fatalf("codex-cli id = %q, want codex-cli-session", cli.ID)
	}
	if cli.Workspace != "/work/cli" {
		t.Fatalf("codex-cli workspace = %q, want /work/cli", cli.Workspace)
	}
	if cli.Title != "cli prompt" {
		t.Fatalf("codex-cli title = %q, want cli prompt", cli.Title)
	}

	app := byAgent["codex-app"]
	if app.ID != "codex-app-session" {
		t.Fatalf("codex-app id = %q, want codex-app-session", app.ID)
	}
	if app.Workspace != "/work/app" {
		t.Fatalf("codex-app workspace = %q, want /work/app", app.Workspace)
	}
	if app.Title != "app prompt" {
		t.Fatalf("codex-app title = %q, want app prompt", app.Title)
	}
}

func TestCodexGoals(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "goal.jsonl")
	writeCollectorJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-03T12:45:09Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":         "goal-session",
				"cwd":        "/work/goal",
				"originator": "codex-tui",
				"source":     "cli",
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:10Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "Continue working toward the active thread goal.\n\n<untrusted_objective>\nship goal support\n</untrusted_objective>\n\nBudget:\n- Time spent pursuing goal: 12 seconds\n- Tokens used: 34\n- Token budget: none"},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:11Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "update_goal",
				"call_id":   "call-goal",
				"arguments": `{"status":"complete"}`,
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:12Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-goal",
				"output":  `{"goal":{"threadId":"goal-session","objective":"ship goal support","status":"complete","tokensUsed":99,"timeUsedSeconds":88,"createdAt":1777812309,"updatedAt":1777812397},"completionBudgetReport":"Goal achieved. Report final budget usage to the user: time used: 88 seconds."}`,
			},
		},
		map[string]any{
			"timestamp": "2026-05-03T12:45:13Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "Still blocked on external evidence.\n\nMissing completion gates:\n- AC-valid focused Metal trace\n- 35B 3-run TPS result\n- 27B and 4B regression checks\n\nReady evidence/prep:\n- harness syntax preflight passed"},
				},
			},
		},
	)

	sess, err := (&Codex{}).parseSession(path)
	if err != nil {
		t.Fatalf("parseSession: %v", err)
	}
	if len(sess.Goals) != 1 {
		t.Fatalf("goals = %d, want 1: %#v", len(sess.Goals), sess.Goals)
	}
	g := sess.Goals[0]
	if g.Objective != "ship goal support" {
		t.Fatalf("objective = %q", g.Objective)
	}
	if g.Status != "complete" {
		t.Fatalf("status = %q, want complete", g.Status)
	}
	if g.TokensUsed != 99 || g.TimeUsedSeconds != 88 {
		t.Fatalf("usage = tokens %d seconds %d", g.TokensUsed, g.TimeUsedSeconds)
	}
	if g.ThreadID != "goal-session" {
		t.Fatalf("thread id = %q", g.ThreadID)
	}
	if !hasGoalGate(g, "AC-valid focused Metal trace", "missing") {
		t.Fatalf("missing AC gate not parsed: %#v", g.CompletionGates)
	}
	if !hasGoalGate(g, "harness syntax preflight passed", "complete") {
		t.Fatalf("ready gate not parsed: %#v", g.CompletionGates)
	}
	if !hasGoalGate(g, "Still blocked on external evidence.", "blocked") {
		t.Fatalf("blocked gate not parsed: %#v", g.CompletionGates)
	}
}

func hasGoalGate(g cass.Goal, name, status string) bool {
	for _, gate := range g.CompletionGates {
		if gate.Name == name && gate.Status == status {
			return true
		}
	}
	return false
}

func TestCodexGoalCompletionGatesMultipleObjectives(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "goals.jsonl")
	writeCollectorJSONL(t, path,
		map[string]any{
			"timestamp": "2026-05-04T17:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":         "019deddc-b7dc-75a2-a393-52ded8ebe04a",
				"cwd":        "/work/goal",
				"originator": "codex-tui",
				"source":     "cli",
			},
		},
		map[string]any{
			"timestamp": "2026-05-04T17:00:01Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "Continue working toward the active thread goal.\n\n<untrusted_objective>\n/tmp/dflash-nlm-v2/codex-goal-d9a2.md\n</untrusted_objective>\n\nBudget:\n- Time spent pursuing goal: 0 seconds\n- Tokens used: 0\n- Token budget: none\n\nBefore deciding that the goal is achieved, perform a completion audit against the actual current state:\n- Restate the objective as concrete deliverables or success criteria."},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-04T17:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-get-goal",
				"output":  `{"goal":{"threadId":"019deddc-b7dc-75a2-a393-52ded8ebe04a","objective":"/tmp/dflash-nlm-v2/codex-goal-d9a2.md","status":"active","tokensUsed":19166,"timeUsedSeconds":21,"createdAt":1777812309,"updatedAt":1777812330}}`,
			},
		},
		map[string]any{
			"timestamp": "2026-05-04T17:15:24Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "developer",
				"content": []map[string]any{
					{"type": "input_text", "text": "Continue working toward the active thread goal.\n\n<untrusted_objective>\nPush Qwen3.6-35B-A3B DFlash-linear to >=1.00x Go/Python median TPS without regressing 27B DDTree-4 (>=1.10x) or 4B DFlash-linear (>=1.05x).\n</untrusted_objective>\n\nBudget:\n- Time spent pursuing goal: 1775 seconds\n- Tokens used: 499888\n- Token budget: none\n\nBefore deciding that the goal is achieved, perform a completion audit against the actual current state:\n- Build a prompt-to-artifact checklist that maps every explicit requirement to concrete evidence."},
				},
			},
		},
		map[string]any{
			"timestamp": "2026-05-04T17:20:53Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": "Objective is still not achievable in the current state because the required AC-gated evidence is missing.\n\nMissing completion gates:\n- AC-valid focused Metal trace\n- 35B 3-run Go/Python TPS >= 1.00x\n- 27B >= 1.10x and 4B >= 1.05x regression checks\n\nNo update_goal: the objective is not complete."},
				},
			},
		},
	)

	sess, err := (&Codex{}).parseSession(path)
	if err != nil {
		t.Fatalf("parseSession: %v", err)
	}
	if len(sess.Goals) != 2 {
		t.Fatalf("goals = %d, want 2: %#v", len(sess.Goals), sess.Goals)
	}
	if hasGoalGate(sess.Goals[0], "AC-valid focused Metal trace", "missing") {
		t.Fatalf("missing gate attached to old objective: %#v", sess.Goals[0].CompletionGates)
	}
	if !hasGoalGate(sess.Goals[1], "AC-valid focused Metal trace", "missing") {
		t.Fatalf("missing gate not attached to active objective: %#v", sess.Goals[1].CompletionGates)
	}
}

func writeCollectorJSONL(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s): %v", path, err)
	}
	defer f.Close()
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
}
