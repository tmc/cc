package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

// jsonlEntry is a minimal helper for building synthetic JSONL lines.
type jsonlEntry map[string]any

func writeJSONL(t *testing.T, path string, lines []jsonlEntry) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, line := range lines {
		if err := enc.Encode(line); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// scanCollector runs ClaudeCode.Scan against the given root and returns
// the emitted sessions. Buffered channel + helper goroutine matches the
// real collector lifecycle (close-on-return).
func scanCollector(t *testing.T, root string) []cass.Session {
	t.Helper()
	c := &ClaudeCode{Root: root}
	out := make(chan cass.Session, 16)
	go func() {
		_ = c.Scan(context.Background(), cass.ScanConfig{}, out)
	}()
	var got []cass.Session
	for s := range out {
		got = append(got, s)
	}
	return got
}

func writeMeta(t *testing.T, path string, agentType, description string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{
		"agentType":   agentType,
		"description": description,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// taskNotificationXML returns a valid <task-notification> blob for the
// given fields. Empty fields are simply omitted.
func taskNotificationXML(taskID, toolUseID, status string, totalTokens, toolUses int, durationMs int64, worktreePath, worktreeBranch string) string {
	out := "<task-notification>"
	if taskID != "" {
		out += "<task-id>" + taskID + "</task-id>"
	}
	if toolUseID != "" {
		out += "<tool-use-id>" + toolUseID + "</tool-use-id>"
	}
	if status != "" {
		out += "<status>" + status + "</status>"
	}
	out += "<usage>"
	out += "<total_tokens>" + itoa(totalTokens) + "</total_tokens>"
	out += "<tool_uses>" + itoa(toolUses) + "</tool_uses>"
	out += "<duration_ms>" + itoa64(durationMs) + "</duration_ms>"
	out += "</usage>"
	if worktreePath != "" || worktreeBranch != "" {
		out += "<worktree>"
		if worktreePath != "" {
			out += "<worktreePath>" + worktreePath + "</worktreePath>"
		}
		if worktreeBranch != "" {
			out += "<worktreeBranch>" + worktreeBranch + "</worktreeBranch>"
		}
		out += "</worktree>"
	}
	out += "</task-notification>"
	return out
}

func itoa(n int) string     { return formatInt(int64(n)) }
func itoa64(n int64) string { return formatInt(n) }

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// findSession returns the unique session emitted by scanCollector. Fails
// the test if zero or more than one session is found.
func findSession(t *testing.T, sessions []cass.Session) cass.Session {
	t.Helper()
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	return sessions[0]
}

func findRun(t *testing.T, runs []cass.SubagentRun, agentID string) cass.SubagentRun {
	t.Helper()
	for _, r := range runs {
		if r.AgentID == agentID {
			return r
		}
	}
	t.Fatalf("subagent run %q not found among %d runs", agentID, len(runs))
	return cass.SubagentRun{}
}

// projectRoot returns root + "/<encoded>" where <encoded> is the
// Claude-style encoded path.
func projectRoot(root, encoded string) string {
	return filepath.Join(root, encoded)
}

func TestExtractSubagentRuns_HappyPath(t *testing.T) {
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "11111111-1111-1111-1111-111111111111"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)

	// Two subagents.
	agentA := "aaaa11"
	agentB := "bbbb22"

	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "kick off"}},
		// enqueue notification for A
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(60 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
			"content":   taskNotificationXML(agentA, "toolu_a", "completed", 22407, 8, 53342, "", ""),
		},
		// dequeue
		{
			"type":      "queue-operation",
			"operation": "dequeue",
			"timestamp": t0.Add(70 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
		},
		// enqueue notification for B (with worktree)
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(120 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
			"content":   taskNotificationXML(agentB, "toolu_b", "completed", 74674, 55, 249945, "/tmp/worktree", "wb-branch"),
		},
		{
			"type":      "queue-operation",
			"operation": "dequeue",
			"timestamp": t0.Add(125 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
		},
	}
	writeJSONL(t, parentPath, parent)

	// Subagent A JSONL + meta.
	subA := []jsonlEntry{
		{"type": "user", "uuid": "sa1", "sessionId": parentID, "agentId": agentA, "isSidechain": true, "timestamp": t0.Add(10 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "do thing"}},
		{"type": "assistant", "uuid": "sa2", "sessionId": parentID, "agentId": agentA, "timestamp": t0.Add(50 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "model": "claude-haiku-4-5", "content": []any{map[string]any{"type": "text", "text": "ok"}}}},
	}
	writeJSONL(t, filepath.Join(proj, parentID, "subagents", "agent-"+agentA+".jsonl"), subA)
	writeMeta(t, filepath.Join(proj, parentID, "subagents", "agent-"+agentA+".meta.json"), "general-purpose", "Do thing A")

	// Subagent B JSONL + meta.
	subB := []jsonlEntry{
		{"type": "user", "uuid": "sb1", "sessionId": parentID, "agentId": agentB, "isSidechain": true, "timestamp": t0.Add(80 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "do other"}},
		{"type": "assistant", "uuid": "sb2", "sessionId": parentID, "agentId": agentB, "timestamp": t0.Add(115 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "model": "claude-opus-4-6", "content": []any{map[string]any{"type": "text", "text": "done"}}}},
	}
	writeJSONL(t, filepath.Join(proj, parentID, "subagents", "agent-"+agentB+".jsonl"), subB)
	writeMeta(t, filepath.Join(proj, parentID, "subagents", "agent-"+agentB+".meta.json"), "general-purpose", "Do thing B")

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)

	if got, want := len(sess.Subagents), 2; got != want {
		t.Fatalf("len(Subagents) = %d, want %d", got, want)
	}

	a := findRun(t, sess.Subagents, agentA)
	if a.ParentSessionID != sess.ID {
		t.Errorf("A.ParentSessionID = %q, want %q", a.ParentSessionID, sess.ID)
	}
	if a.ParentClaudeSID != parentID {
		t.Errorf("A.ParentClaudeSID = %q, want %q", a.ParentClaudeSID, parentID)
	}
	if a.AgentType != "general-purpose" || a.Description != "Do thing A" {
		t.Errorf("A meta = (%q, %q), want (general-purpose, Do thing A)", a.AgentType, a.Description)
	}
	if a.Model != "claude-haiku-4-5" {
		t.Errorf("A.Model = %q, want claude-haiku-4-5", a.Model)
	}
	if a.TotalTokens != 22407 || a.ToolUses != 8 || a.DurationMs != 53342 {
		t.Errorf("A usage = (%d, %d, %d), want (22407, 8, 53342)", a.TotalTokens, a.ToolUses, a.DurationMs)
	}
	if a.Status != "completed" {
		t.Errorf("A.Status = %q, want completed", a.Status)
	}
	if a.ToolUseID != "toolu_a" {
		t.Errorf("A.ToolUseID = %q, want toolu_a", a.ToolUseID)
	}
	if a.EnqueuedAt.IsZero() || a.DequeuedAt.IsZero() {
		t.Errorf("A queue ts unset: enq=%v deq=%v", a.EnqueuedAt, a.DequeuedAt)
	}
	if !a.EnqueuedAt.Before(a.DequeuedAt) {
		t.Errorf("A: EnqueuedAt %v not before DequeuedAt %v", a.EnqueuedAt, a.DequeuedAt)
	}
	if a.StartedAt.IsZero() || a.EndedAt.IsZero() || !a.StartedAt.Before(a.EndedAt) {
		t.Errorf("A: started/ended invalid: started=%v ended=%v", a.StartedAt, a.EndedAt)
	}
	if a.EntryCount != 2 {
		t.Errorf("A.EntryCount = %d, want 2", a.EntryCount)
	}

	b := findRun(t, sess.Subagents, agentB)
	if b.WorktreePath != "/tmp/worktree" || b.WorktreeBranch != "wb-branch" {
		t.Errorf("B worktree = (%q, %q), want (/tmp/worktree, wb-branch)", b.WorktreePath, b.WorktreeBranch)
	}
	if b.TotalTokens != 74674 {
		t.Errorf("B.TotalTokens = %d, want 74674", b.TotalTokens)
	}
}

func TestExtractSubagentRuns_AcompactOnly(t *testing.T) {
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "22222222-2222-2222-2222-222222222222"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)

	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "hi"}},
	}
	writeJSONL(t, parentPath, parent)

	// Only an acompact subagent — it is indexed as compaction metadata, not
	// merged into the parent transcript or graph fan-out.
	acomp := []jsonlEntry{
		{"type": "user", "uuid": "x1", "sessionId": parentID, "agentId": "acompact-aaa", "isSidechain": true, "timestamp": t0.Add(10 * time.Second).Format(time.RFC3339Nano)},
		{"type": "assistant", "uuid": "x2", "sessionId": parentID, "agentId": "acompact-aaa", "isSidechain": true, "timestamp": t0.Add(20 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "model": "claude-haiku-4-5", "content": "summary"}},
	}
	writeJSONL(t, filepath.Join(proj, parentID, "subagents", "agent-acompact-aaa.jsonl"), acomp)

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)
	if len(sess.Subagents) != 1 {
		t.Fatalf("Subagents = %d, want 1 compaction record", len(sess.Subagents))
	}
	r := sess.Subagents[0]
	if !r.IsCompaction {
		t.Errorf("IsCompaction = false, want true")
	}
	if r.AgentID != "acompact-aaa" {
		t.Errorf("AgentID = %q, want acompact-aaa", r.AgentID)
	}
	if r.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", r.Model)
	}
	if r.EntryCount != 2 {
		t.Errorf("EntryCount = %d, want 2", r.EntryCount)
	}
}

func TestExtractSubagentRuns_OrphanQueueOp(t *testing.T) {
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "33333333-3333-3333-3333-333333333333"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)

	// queue-op references an agent file that does not exist.
	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "hi"}},
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(60 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
			"content":   taskNotificationXML("ghostagent", "toolu_g", "completed", 100, 1, 1000, "", ""),
		},
	}
	writeJSONL(t, parentPath, parent)

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)
	if len(sess.Subagents) != 0 {
		t.Errorf("orphan queue-op must not produce SubagentRun (no JSONL), got %d", len(sess.Subagents))
	}
}

func TestExtractSubagentRuns_MissingMeta(t *testing.T) {
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "44444444-4444-4444-4444-444444444444"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	agent := "abc123"

	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "hi"}},
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(60 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
			"content":   taskNotificationXML(agent, "toolu_x", "completed", 5, 1, 100, "", ""),
		},
	}
	writeJSONL(t, parentPath, parent)

	sub := []jsonlEntry{
		{"type": "user", "uuid": "s1", "sessionId": parentID, "agentId": agent, "isSidechain": true, "timestamp": t0.Add(10 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "x"}},
	}
	writeJSONL(t, filepath.Join(proj, parentID, "subagents", "agent-"+agent+".jsonl"), sub)
	// No meta.json sidecar.

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)
	if len(sess.Subagents) != 1 {
		t.Fatalf("want 1 SubagentRun, got %d", len(sess.Subagents))
	}
	r := sess.Subagents[0]
	if r.AgentType != "" || r.Description != "" {
		t.Errorf("missing meta should leave AgentType/Description empty, got (%q, %q)", r.AgentType, r.Description)
	}
	if r.MetaPath != "" {
		t.Errorf("MetaPath should be empty when sidecar absent, got %q", r.MetaPath)
	}
	if r.TotalTokens != 5 || r.Status != "completed" {
		t.Errorf("notification fields lost: tokens=%d status=%q", r.TotalTokens, r.Status)
	}
}

func TestExtractSubagentRuns_ForkParentSessionIDIsCassID(t *testing.T) {
	// Critical regression guard: SubagentRun.ParentSessionID must be the
	// cass session ID (sha256 of the JSONL path), NOT the Claude
	// sessionId. A fork shares the original Claude sessionId in the
	// header but lives in a different file with a different cass ID.
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")

	originalClaudeID := "55555555-5555-5555-5555-555555555555"
	forkClaudeID := "66666666-6666-6666-6666-666666666666"
	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)

	// Original session with one subagent.
	origPath := filepath.Join(proj, originalClaudeID+".jsonl")
	agent := "shared99"
	writeJSONL(t, origPath, []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": originalClaudeID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "orig"}},
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(30 * time.Second).Format(time.RFC3339Nano),
			"sessionId": originalClaudeID,
			"content":   taskNotificationXML(agent, "toolu_o", "completed", 10, 1, 100, "", ""),
		},
	})
	writeJSONL(t, filepath.Join(proj, originalClaudeID, "subagents", "agent-"+agent+".jsonl"), []jsonlEntry{
		{"type": "user", "uuid": "s1", "sessionId": originalClaudeID, "agentId": agent, "isSidechain": true, "timestamp": t0.Add(10 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "x"}},
	})

	// Fork session — different file, different cass ID, different Claude
	// sessionId in header. Has its own (different) subagent dir.
	forkPath := filepath.Join(proj, forkClaudeID+".jsonl")
	forkAgent := "forkagt"
	writeJSONL(t, forkPath, []jsonlEntry{
		{"type": "user", "uuid": "fu1", "sessionId": forkClaudeID, "timestamp": t0.Add(time.Hour).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "fork"}},
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(time.Hour + 30*time.Second).Format(time.RFC3339Nano),
			"sessionId": forkClaudeID,
			"content":   taskNotificationXML(forkAgent, "toolu_f", "completed", 20, 2, 200, "", ""),
		},
	})
	writeJSONL(t, filepath.Join(proj, forkClaudeID, "subagents", "agent-"+forkAgent+".jsonl"), []jsonlEntry{
		{"type": "user", "uuid": "fs1", "sessionId": forkClaudeID, "agentId": forkAgent, "isSidechain": true, "timestamp": t0.Add(time.Hour + 10*time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "y"}},
	})

	sessions := scanCollector(t, root)
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}

	var origSess, forkSess cass.Session
	for _, s := range sessions {
		switch s.SourcePath {
		case origPath:
			origSess = s
		case forkPath:
			forkSess = s
		}
	}
	if origSess.ID == "" || forkSess.ID == "" {
		t.Fatalf("missing session: orig=%+v fork=%+v", origSess, forkSess)
	}
	if origSess.ID == forkSess.ID {
		t.Fatalf("orig and fork share cass ID %q — sessionID() should differ by path", origSess.ID)
	}

	r1 := findRun(t, origSess.Subagents, agent)
	if r1.ParentSessionID != origSess.ID {
		t.Errorf("orig run parent_session_id = %q, want %q (cass ID, not Claude sessionId)", r1.ParentSessionID, origSess.ID)
	}
	r2 := findRun(t, forkSess.Subagents, forkAgent)
	if r2.ParentSessionID != forkSess.ID {
		t.Errorf("fork run parent_session_id = %q, want %q", r2.ParentSessionID, forkSess.ID)
	}
	if r1.ParentSessionID == r2.ParentSessionID {
		t.Errorf("orig and fork runs share parent_session_id — keying off Claude sessionId, not cass ID")
	}
}

func TestExtractSubagentRuns_HumanInputEnqueue(t *testing.T) {
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "77777777-7777-7777-7777-777777777777"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)

	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "hi"}},
		// human-input enqueue: plain free text, no <task-notification>.
		{
			"type":      "queue-operation",
			"operation": "enqueue",
			"timestamp": t0.Add(60 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
			"content":   "what about chrome-to-har dimensions",
		},
		{
			"type":      "queue-operation",
			"operation": "remove",
			"timestamp": t0.Add(65 * time.Second).Format(time.RFC3339Nano),
			"sessionId": parentID,
		},
	}
	writeJSONL(t, parentPath, parent)

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)
	if len(sess.Subagents) != 0 {
		t.Errorf("human-input enqueue must not yield SubagentRun, got %d", len(sess.Subagents))
	}
}

func TestExtractSubagentRuns_MissingNotificationStatusUnknown(t *testing.T) {
	// JSONL exists but no queue-op enqueue — Q3 default a:
	// emit run with Status="unknown" and zero usage.
	root := t.TempDir()
	proj := projectRoot(root, "-tmp-fixture")
	parentID := "88888888-8888-8888-8888-888888888888"
	parentPath := filepath.Join(proj, parentID+".jsonl")

	t0 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	agent := "noqop1"

	parent := []jsonlEntry{
		{"type": "user", "uuid": "u1", "sessionId": parentID, "timestamp": t0.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "hi"}},
	}
	writeJSONL(t, parentPath, parent)
	sub := []jsonlEntry{
		{"type": "user", "uuid": "s1", "sessionId": parentID, "agentId": agent, "isSidechain": true, "timestamp": t0.Add(10 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "x"}},
	}
	writeJSONL(t, filepath.Join(proj, parentID, "subagents", "agent-"+agent+".jsonl"), sub)

	sessions := scanCollector(t, root)
	sess := findSession(t, sessions)
	if len(sess.Subagents) != 1 {
		t.Fatalf("want 1 SubagentRun, got %d", len(sess.Subagents))
	}
	r := sess.Subagents[0]
	if r.Status != "unknown" {
		t.Errorf("Status = %q, want unknown", r.Status)
	}
	if r.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d, want 0 when notification missing", r.TotalTokens)
	}
	if !r.EnqueuedAt.IsZero() || !r.DequeuedAt.IsZero() {
		t.Errorf("queue timestamps should be zero when notification missing")
	}
	if r.StartedAt.IsZero() {
		t.Errorf("StartedAt should still come from JSONL even without notification")
	}
}
