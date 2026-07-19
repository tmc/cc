package collector

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// assistantTextEntry builds an assistant entry with a single text block.
func assistantTextEntry(text string, ts time.Time) cc.Entry {
	blocks := []cc.ContentBlock{{Type: "text", Text: text}}
	content, _ := json.Marshal(blocks)
	return cc.Entry{
		Timestamp: ts,
		Message:   &cc.Message{Role: "assistant", Content: content},
	}
}

// artifactResultEntry builds an entry carrying an Artifact tool result.
func artifactResultEntry(url, path, title string, ts time.Time) cc.Entry {
	return cc.Entry{
		Timestamp: ts,
		ToolUseResult: &cc.ToolUseResult{
			URL:   url,
			Path:  path,
			Title: title,
		},
	}
}

func TestExtractArtifacts(t *testing.T) {
	ts := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	const (
		artifactURL = "https://claude.ai/code/artifact/1ea3aecb-9902-49a8-94c9-443eeda133f5"
		sessionURL  = "https://claude.ai/code/session_011vNeoaAt8eZGAi7MWbQtnn"
		designURL   = "https://claude.ai/design/p"
		publicURL   = "https://claude.ai/public/artifacts/4ad70609-2540-4a34-bd81-8f151278ecf4"
	)

	tests := []struct {
		name      string
		entries   []cc.Entry
		wantCount int
		check     func(t *testing.T, arts []cass.Artifact)
	}{
		{
			name: "artifact tool result fully populated",
			entries: []cc.Entry{
				artifactResultEntry(artifactURL, "/tmp/scratch/report.html", "Decode Sweep — Results", ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				a := arts[0]
				if a.Kind != cass.ArtifactKindArtifact {
					t.Errorf("kind = %q, want %q", a.Kind, cass.ArtifactKindArtifact)
				}
				if a.ID != "1ea3aecb-9902-49a8-94c9-443eeda133f5" {
					t.Errorf("id = %q", a.ID)
				}
				if a.Path != "/tmp/scratch/report.html" {
					t.Errorf("path = %q", a.Path)
				}
				if a.Title != "Decode Sweep — Results" {
					t.Errorf("title = %q", a.Title)
				}
				if a.Source != "tool_result" {
					t.Errorf("source = %q, want tool_result", a.Source)
				}
				if a.Timestamp != "2025-01-15T10:30:00Z" {
					t.Errorf("timestamp = %q", a.Timestamp)
				}
			},
		},
		{
			name: "session link in commit command",
			entries: []cc.Entry{
				bashToolUseEntry("assistant", `git commit -m "fix: thing

Claude-Session: `+sessionURL+`"`, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].Kind != cass.ArtifactKindSession {
					t.Errorf("kind = %q, want session", arts[0].Kind)
				}
				if arts[0].ID != "session_011vNeoaAt8eZGAi7MWbQtnn" {
					t.Errorf("id = %q", arts[0].ID)
				}
				if arts[0].Source != "command" {
					t.Errorf("source = %q, want command", arts[0].Source)
				}
			},
		},
		{
			name: "artifact link in assistant text",
			entries: []cc.Entry{
				assistantTextEntry("Published at "+artifactURL+" for review.", ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].Source != "text" {
					t.Errorf("source = %q, want text", arts[0].Source)
				}
				if arts[0].URL != artifactURL {
					t.Errorf("url = %q", arts[0].URL)
				}
			},
		},
		{
			name: "design link classified",
			entries: []cc.Entry{
				assistantTextEntry("See "+designURL, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].Kind != cass.ArtifactKindDesign {
					t.Errorf("kind = %q, want design", arts[0].Kind)
				}
			},
		},
		{
			name: "public artifact classified",
			entries: []cc.Entry{
				assistantTextEntry("shared: "+publicURL, ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].Kind != cass.ArtifactKindPublic {
					t.Errorf("kind = %q, want public_artifact", arts[0].Kind)
				}
				if arts[0].ID != "4ad70609-2540-4a34-bd81-8f151278ecf4" {
					t.Errorf("id = %q", arts[0].ID)
				}
			},
		},
		{
			name: "tool result preferred over later text mention",
			entries: []cc.Entry{
				artifactResultEntry(artifactURL, "/tmp/r.html", "Report", ts),
				assistantTextEntry("Reminder: "+artifactURL, ts.Add(time.Minute)),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].Source != "tool_result" || arts[0].Path == "" {
					t.Errorf("expected tool_result with path to win, got %+v", arts[0])
				}
			},
		},
		{
			name: "user text mentions are ignored",
			entries: []cc.Entry{
				func() cc.Entry {
					e := assistantTextEntry("look at "+artifactURL, ts)
					e.Message.Role = "user"
					return e
				}(),
			},
			wantCount: 0,
		},
		{
			name: "surrounding delimiters not captured",
			entries: []cc.Entry{
				assistantTextEntry("(see `"+artifactURL+"`) done", ts),
			},
			wantCount: 1,
			check: func(t *testing.T, arts []cass.Artifact) {
				if arts[0].URL != artifactURL {
					t.Errorf("url = %q, want clean url without delimiters", arts[0].URL)
				}
			},
		},
		{
			name: "multiple distinct links in one text",
			entries: []cc.Entry{
				assistantTextEntry(artifactURL+" and "+sessionURL, ts),
			},
			wantCount: 2,
		},
		{
			name:      "empty entries",
			entries:   nil,
			wantCount: 0,
		},
		{
			name: "non-claude url ignored",
			entries: []cc.Entry{
				assistantTextEntry("https://example.com/code/artifact/abc", ts),
			},
			wantCount: 0,
		},
		{
			name: "non-claude tool result with matching path segment ignored",
			entries: []cc.Entry{
				// A WebFetch-style result whose url contains "/code/artifact/"
				// but is not a claude.ai host must not be classified.
				artifactResultEntry("https://evil.example.com/code/artifact/hijack", "/tmp/x", "spoof", ts),
			},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			arts := ExtractArtifacts(tt.entries)
			if len(arts) != tt.wantCount {
				t.Fatalf("got %d artifacts, want %d: %+v", len(arts), tt.wantCount, arts)
			}
			if tt.check != nil {
				tt.check(t, arts)
			}
		})
	}
}

func TestArtifactID(t *testing.T) {
	tests := []struct {
		url, want string
	}{
		{"https://claude.ai/code/artifact/1ea3aecb-9902", "1ea3aecb-9902"},
		{"https://claude.ai/public/artifacts/4ad70609/raw", "4ad70609"},
		{"https://claude.ai/code/session_011vNeoaAt8eZGAi7MWbQtnn", "session_011vNeoaAt8eZGAi7MWbQtnn"},
		{"https://claude.ai/design/p/77767204-16d4", "p"},
		{"https://claude.ai/design/slug/", "slug"},
	}
	for _, tt := range tests {
		if got := artifactID(tt.url); got != tt.want {
			t.Errorf("artifactID(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
