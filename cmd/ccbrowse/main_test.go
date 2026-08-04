package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmc/cc"
)

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", "''"},
		{"abc-123_./:", "abc-123_./:"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestInProject(t *testing.T) {
	tests := []struct {
		name string
		sum  cc.SessionSummary
		cwd  string
		want bool
	}{
		{"exact", cc.SessionSummary{CWD: "/a/b"}, "/a/b", true},
		{"below", cc.SessionSummary{CWD: "/a/b/c"}, "/a/b", true},
		{"sibling prefix", cc.SessionSummary{CWD: "/a/bc"}, "/a/b", false},
		{"distinct cwds", cc.SessionSummary{DistinctCWDs: []string{"/x", "/a/b"}}, "/a/b", true},
		{"unrelated", cc.SessionSummary{CWD: "/x"}, "/a/b", false},
	}
	for _, tt := range tests {
		if got := inProject(tt.sum, tt.cwd); got != tt.want {
			t.Errorf("%s: inProject = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestRowMatches(t *testing.T) {
	r := row{sum: cc.SessionSummary{
		SessionID:   "00000011-4d32",
		Project:     "cc",
		FirstPrompt: "Fix the reader bug",
	}}
	for _, q := range []string{r.sum.SessionID[:4], "reader bug", "cc"} {
		if !rowMatches(r, q) {
			t.Errorf("rowMatches(%q) = false, want true", q)
		}
	}
	if rowMatches(r, "zebra") {
		t.Error(`rowMatches("zebra") = true, want false`)
	}
}

func TestRenderTranscript(t *testing.T) {
	mkmsg := func(role, content string) *cc.Message {
		return &cc.Message{Role: role, Content: json.RawMessage(content)}
	}
	entries := []cc.Entry{
		{Type: "user", Message: mkmsg("user", `"hello there"`)},
		{Type: "assistant", Message: mkmsg("assistant",
			`[{"type":"text","text":"hi back"},{"type":"tool_use","name":"Bash","input":{"command":"ls -l"}}]`)},
		{Type: "user", IsMeta: true, Message: mkmsg("user", `"meta noise"`)},
	}
	out := renderTranscript("x.jsonl", entries, false, 0)
	for _, want := range []string{"hello there", "hi back", "Bash", "ls -l"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderTranscript missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "meta noise") {
		t.Error("renderTranscript included IsMeta entry")
	}
}

func TestResumeCommand(t *testing.T) {
	r := row{
		file: "/home/u/.claude/projects/-a-b/1234.jsonl",
		sum:  cc.SessionSummary{SessionID: "1234", CWD: "/a/b"},
	}
	got := resumeCommand(r)
	if want := "cd /a/b; claude -r 1234"; got != want {
		t.Errorf("resumeCommand = %q, want %q", got, want)
	}
}
