package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tmc/cc/cass/store"
)

func TestScanJobs(t *testing.T) {
	dir := t.TempDir()
	short := "deadbeef"
	jd := filepath.Join(dir, short)
	if err := os.MkdirAll(jd, 0o755); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{
		"state":     "done",
		"sessionId": "deadbeef-0000-0000-0000-000000000000",
		"name":      "x",
		"backend":   "daemon",
		"output":    map[string]any{"result": "ok"},
	}
	b, _ := json.Marshal(state)
	os.WriteFile(filepath.Join(jd, "state.json"), b, 0o644)
	os.WriteFile(filepath.Join(jd, "timeline.jsonl"), []byte(`{"at":"2026-05-12T00:00:00Z","state":"done","text":"t"}`+"\n"), 0o644)

	jobs, err := ScanJobs(dir)
	if err != nil {
		t.Fatalf("ScanJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d want 1", len(jobs))
	}
	got := jobs[0]
	if got.ShortID != short || got.SessionID != state["sessionId"] || got.OutputResult != "ok" || got.EventCount != 1 {
		t.Errorf("unexpected job: %+v", got)
	}

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveJob(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	all, err := st.Jobs(context.Background())
	if err != nil || len(all) != 1 || all[0].ShortID != short {
		t.Fatalf("Jobs round-trip mismatch: %v %+v", err, all)
	}
}

func TestScanAgentDefs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".disabled"), 0o755)
	os.WriteFile(filepath.Join(dir, "alpha.json"), []byte(`{"name":"alpha","tools":["Bash"]}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".disabled", "beta.json"), []byte(`{"name":"beta"}`), 0o644)

	defs, err := ScanAgentDefs(dir)
	if err != nil {
		t.Fatalf("ScanAgentDefs: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("got %d want 2", len(defs))
	}
	byName := map[string]store.AgentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	if a := byName["alpha"]; a.Disabled || a.ToolsJSON != `["Bash"]` {
		t.Errorf("alpha unexpected: %+v", a)
	}
	if b := byName["beta"]; !b.Disabled {
		t.Errorf("beta should be disabled: %+v", b)
	}

	st, _ := store.New(":memory:")
	defer st.Close()
	for _, d := range defs {
		if err := st.SaveAgentDef(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.AgentDefs(context.Background())
	if len(got) != 2 {
		t.Fatalf("AgentDefs len=%d want 2", len(got))
	}
}

func TestScanTeamConfigsIndexesMembers(t *testing.T) {
	dir := t.TempDir()
	teamDir := filepath.Join(dir, "review")
	if err := os.MkdirAll(teamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{
		"name": "review",
		"description": "review team",
		"createdAt": 1770000000000,
		"leadAgentId": "lead@review",
		"leadSessionId": "lead-session",
		"members": [{
			"agentId": "worker@review",
			"name": "worker",
			"agentType": "reviewer",
			"model": "claude-opus-4-6",
			"color": "blue",
			"prompt": "review carefully",
			"backendType": "tmux",
			"isActive": true,
			"joinedAt": 1770000001000,
			"tmuxPaneId": "%12",
			"cwd": "/work/repo",
			"subscriptions": ["lead"]
		}]
	}`
	if err := os.WriteFile(filepath.Join(teamDir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	configs, err := ScanTeamConfigs(dir)
	if err != nil {
		t.Fatalf("ScanTeamConfigs: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("configs = %d, want 1", len(configs))
	}
	tc := configs[0]
	if tc.Name != "review" || tc.CreatedAt != 1770000000 || len(tc.Members) != 1 {
		t.Fatalf("team config = %+v, want parsed member", tc)
	}
	member := tc.Members[0]
	if member.AgentID != "worker@review" || member.AgentType != "reviewer" || member.Model != "claude-opus-4-6" {
		t.Fatalf("member = %+v, want normalized fields", member)
	}
	if member.JoinedAt != 1770000001 || member.TmuxPaneID != "%12" || member.SubscriptionsJSON != `["lead"]` {
		t.Fatalf("member timing/pane/subscriptions = %+v", member)
	}

	st, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveTeamConfig(context.Background(), tc); err != nil {
		t.Fatal(err)
	}
	members, err := st.TeamMembers(context.Background(), "review")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("TeamMembers = %d, want 1", len(members))
	}
	got := members[0]
	if got.AgentID != "worker@review" || got.Prompt != "review carefully" || !got.IsActive {
		t.Fatalf("stored member = %+v, want queryable member row", got)
	}
}
