package ccjobstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadJobAndTimeline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CC_JOBS_DIR", dir)

	short := "abcd1234"
	jobDir := filepath.Join(dir, short)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatal(err)
	}

	state := map[string]any{
		"state":     "done",
		"detail":    "ok",
		"sessionId": "abcd1234-5678-90ab-cdef-1234567890ab",
		"createdAt": time.Now().UTC().Format(time.RFC3339Nano),
		"name":      "demo",
		"backend":   "daemon",
		"inFlight":  map[string]any{"tasks": 0, "queued": 0},
		"output":    map[string]any{"result": "all clear"},
	}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(jobDir, "state.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	timeline := `{"at":"2026-05-12T12:00:00Z","state":"running","text":"start"}
{"at":"2026-05-12T12:00:05Z","state":"done","text":"end"}
`
	if err := os.WriteFile(filepath.Join(jobDir, "timeline.jsonl"), []byte(timeline), 0o644); err != nil {
		t.Fatal(err)
	}

	j, err := ReadJob(short)
	if err != nil {
		t.Fatalf("ReadJob: %v", err)
	}
	if j.SessionID != state["sessionId"] {
		t.Errorf("sessionId mismatch: got %q want %q", j.SessionID, state["sessionId"])
	}
	if j.State != "done" || j.Backend != "daemon" || j.Name != "demo" {
		t.Errorf("scalar fields mismatch: %+v", j)
	}
	if got, ok := j.Output["result"].(string); !ok || got != "all clear" {
		t.Errorf("output.result mismatch: %v", j.Output)
	}

	jobs, err := ListJobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs: got %d jobs err=%v", len(jobs), err)
	}
	if jobs[0].ShortID != short {
		t.Errorf("ShortID = %q want %q", jobs[0].ShortID, short)
	}

	events, err := ReadJobTimeline(short)
	if err != nil {
		t.Fatalf("ReadJobTimeline: %v", err)
	}
	if len(events) != 2 || events[0].State != "running" || events[1].Text != "end" {
		t.Errorf("timeline parse mismatch: %+v", events)
	}
}
