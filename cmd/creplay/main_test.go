package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindOpenCodeSessionFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_HOME", filepath.Join(root, "opencode"))
	path := filepath.Join(root, "opencode", "storage", "session", "proj", "ses_replay.json")
	writeReplayFile(t, path, `{"id":"ses_replay","directory":"/work/replay","time":{"created":4102444800000}}`)

	got, ok := findOpenCodeSessionFile("ses_replay")
	if !ok || got != path {
		t.Fatalf("findOpenCodeSessionFile = %q, %v; want %q, true", got, ok, path)
	}
}

func TestLoadMessagesOpenCodeFollow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "opencode", "storage", "session", "proj", "ses_follow.json")
	writeReplayFile(t, path, `{"id":"ses_follow","directory":"/work/replay","time":{"created":4102444800000}}`)
	writeReplayFile(t, filepath.Join(root, "opencode", "storage", "message", "ses_follow", "msg_user.json"), `{"id":"msg_user","sessionID":"ses_follow","role":"user","time":{"created":4102444801000}}`)
	writeReplayFile(t, filepath.Join(root, "opencode", "storage", "part", "msg_user", "prt_user.json"), `{"id":"prt_user","sessionID":"ses_follow","messageID":"msg_user","type":"text","text":"hello replay"}`)

	messages, reader, file, err := loadMessages(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if reader != nil || file != nil {
		t.Fatalf("reader=%v file=%v, want nil opencode follow handles", reader, file)
	}
	if len(messages) != 1 || messages[0].Message == nil || messages[0].Message.Role != "user" {
		t.Fatalf("messages = %#v, want one user message", messages)
	}
}

func writeReplayFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
