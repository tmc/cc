package collector

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiParseSession_ContentFormats(t *testing.T) {
	tests := []struct {
		name        string
		userContent string
		wantPrompt  string
		wantReply   string
	}{
		{
			name:        "legacy string content",
			userContent: `"legacy prompt"`,
			wantPrompt:  "legacy prompt",
			wantReply:   "legacy reply",
		},
		{
			name:        "new array content",
			userContent: `[{"text":"new prompt"}]`,
			wantPrompt:  "new prompt",
			wantReply:   "new reply",
		},
		{
			name:        "nested parts content",
			userContent: `{"parts":[{"text":"parts prompt"}]}`,
			wantPrompt:  "parts prompt",
			wantReply:   "parts reply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()
			gh := filepath.Join(tmp, ".gemini")
			if err := os.MkdirAll(gh, 0o755); err != nil {
				t.Fatalf("mkdir gemini home: %v", err)
			}
			t.Setenv("GEMINI_HOME", gh)

			projectDir := filepath.Join(gh, "tmp", "myproj")
			chatsDir := filepath.Join(projectDir, "chats")
			if err := os.MkdirAll(chatsDir, 0o755); err != nil {
				t.Fatalf("mkdir chats: %v", err)
			}
			workspace := "/tmp/workspace/project"
			if err := os.WriteFile(filepath.Join(projectDir, ".project_root"), []byte(workspace+"\n"), 0o644); err != nil {
				t.Fatalf("write .project_root: %v", err)
			}

			sessionPath := filepath.Join(chatsDir, "session-2026-02-23T14-49-abcd1234.json")
			sessionJSON := `{
				"sessionId":"abcd1234-1111-2222-3333-abcdefabcdef",
				"projectHash":"ignored-when-project-root-exists",
				"startTime":"2026-02-23T14:49:12.779Z",
				"lastUpdated":"2026-02-23T14:50:49.139Z",
				"messages":[
					{
						"id":"u1",
						"timestamp":"2026-02-23T14:49:27.721Z",
						"type":"user",
						"content":` + tt.userContent + `
					},
					{
						"id":"a1",
						"timestamp":"2026-02-23T14:49:38.239Z",
						"type":"gemini",
						"content":"` + tt.wantReply + `",
						"tokens":{"input":12,"output":7,"cached":5},
						"model":"gemini-3-flash-preview"
					}
				]
			}`
			if err := os.WriteFile(sessionPath, []byte(sessionJSON), 0o644); err != nil {
				t.Fatalf("write session: %v", err)
			}

			c := &GeminiCLI{}
			sess, err := c.parseSession(sessionPath)
			if err != nil {
				t.Fatalf("parseSession: %v", err)
			}

			if sess.Agent != "gemini-cli" {
				t.Fatalf("agent = %q, want gemini-cli", sess.Agent)
			}
			if sess.Workspace != workspace {
				t.Fatalf("workspace = %q, want %q", sess.Workspace, workspace)
			}
			if sess.Title != tt.wantPrompt {
				t.Fatalf("title = %q, want %q", sess.Title, tt.wantPrompt)
			}
			if len(sess.Messages) != 2 {
				t.Fatalf("messages len = %d, want 2", len(sess.Messages))
			}
			if sess.Messages[0].Content != tt.wantPrompt {
				t.Fatalf("first message = %q, want %q", sess.Messages[0].Content, tt.wantPrompt)
			}
			if sess.Messages[1].Content != tt.wantReply {
				t.Fatalf("second message = %q, want %q", sess.Messages[1].Content, tt.wantReply)
			}
		})
	}
}
