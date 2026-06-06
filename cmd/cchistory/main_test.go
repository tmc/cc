package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSearchFileOpenCode(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "opencode", "storage", "session", "proj", "ses_hist.json")
	writeHistoryFile(t, sessionPath, `{"id":"ses_hist","directory":"/work/hist","time":{"created":1780000000000}}`)
	writeHistoryFile(t, filepath.Join(root, "opencode", "storage", "message", "ses_hist", "msg_user.json"), `{"id":"msg_user","sessionID":"ses_hist","role":"user","time":{"created":1780000001000}}`)
	writeHistoryFile(t, filepath.Join(root, "opencode", "storage", "part", "msg_user", "prt_user.json"), `{"id":"prt_user","sessionID":"ses_hist","messageID":"msg_user","type":"text","text":"hello opencode history"}`)

	matches, err := searchFile(sessionPath, regexp.MustCompile("opencode history"))
	if err != nil {
		t.Fatalf("searchFile: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1: %#v", len(matches), matches)
	}
	if matches[0].Content != "hello opencode history" {
		t.Fatalf("content = %q, want hello opencode history", matches[0].Content)
	}
}

func TestPatternDoesNotResetLimit(t *testing.T) {
	old := *nFlag
	t.Cleanup(func() { *nFlag = old })
	*nFlag = 100

	var nval int
	if n, err := fmt.Sscanf("opencode", "%d", &nval); n == 1 && err == nil {
		*nFlag = nval
	}
	if *nFlag != 100 {
		t.Fatalf("nFlag = %d, want 100", *nFlag)
	}
}

func writeHistoryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
