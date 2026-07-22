package codexread

import "testing"

func TestDecodeNormalizedSessionMeta(t *testing.T) {
	line := []byte(`{"type":"session_meta","sessionId":"ag-session","source":"antigravity","subtype":"session_meta"}`)
	if _, isCodex, _ := Decode(line); isCodex {
		t.Fatal("Decode classified normalized session metadata as a Codex envelope")
	}
}

func TestDecodeCodexSessionMeta(t *testing.T) {
	line := []byte(`{"timestamp":"2026-07-22T00:00:00Z","type":"session_meta","payload":{"id":"codex-session","cwd":"/tmp/project"}}`)
	entry, isCodex, ok := Decode(line)
	if !isCodex || !ok {
		t.Fatalf("Decode = (_, %v, %v), want (_, true, true)", isCodex, ok)
	}
	if entry.SessionID != "codex-session" || entry.CWD != "/tmp/project" {
		t.Fatalf("entry = %+v", entry)
	}
}
