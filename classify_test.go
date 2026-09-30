package cc

import (
	"encoding/json"
	"testing"
)

func TestClassify(t *testing.T) {
	user := func(text string) Entry {
		data, _ := json.Marshal(text)
		return Entry{Type: "user", Message: &Message{Role: "user", Content: data}}
	}
	tests := []struct {
		name  string
		entry Entry
		want  string
	}{
		{"human", user("fix this"), "human"},
		{"collab protocol belongs to consumer", user(`<collab-msg src="12345678">hello</collab-msg>`), "human"},
		{"peer", user(`<teammate-message teammate_id="reviewer">done</teammate-message>`), "peer"},
		{"task", user(`<task-notification><task-id>a</task-id></task-notification>`), "subagent"},
		{"queue task", Entry{Type: "queue-operation", Content: `<task-notification><task-id>a</task-id></task-notification>`}, "subagent"},
		{"preamble", user("# AGENTS.md instructions\nUse tabs"), "system"},
		{"meta", Entry{IsMeta: true}, "system"},
		{"summary", Entry{IsCompactSummary: true}, "system"},
		{"system", Entry{Type: "system"}, "system"},
		{"result", Entry{Message: &Message{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"a","content":"done"}]`)}}, "tool"},
		{"assistant", Entry{Message: &Message{Role: "assistant", Content: json.RawMessage(`"done"`)}}, ""},
		{"empty", Entry{}, ""},
		{"sidechain", Entry{IsSidechain: true}, "subagent"},
		{"agent", Entry{AgentID: "a"}, "subagent"},
		{"spawned Codex", Entry{ParentThreadID: "parent"}, "subagent"},
		{"mention", user("explain <command-name> tags"), "human"},
	}
	for _, origin := range []struct{ value, want string }{{"human", "human"}, {"peer", "peer"}, {"task-notification", "subagent"}, {"auto-continuation", "system"}, {"subagent", "subagent"}, {"tool", "tool"}, {"command", "command"}, {"system", "system"}, {"unknown", "human"}} {
		entry := user("fix this")
		entry.Origin = origin.value
		tests = append(tests, struct {
			name  string
			entry Entry
			want  string
		}{"origin " + origin.value, entry, origin.want})
	}
	for _, tag := range []string{"command-name", "command-message", "command-args", "local-command-stdout"} {
		tests = append(tests, struct {
			name  string
			entry Entry
			want  string
		}{tag, user("<" + tag + ">value"), "command"})
	}
	entry := user("# AGENTS.md instructions")
	entry.Origin = "human"
	tests = append(tests, struct {
		name  string
		entry Entry
		want  string
	}{"origin precedence", entry, "human"})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.entry); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassificationRulesIndependent(t *testing.T) {
	rules := ClassificationRules()
	rules[0] = func(Entry) string { return "peer" }
	if got := Classify(Entry{Origin: "human"}); got != "human" {
		t.Fatalf("rules mutation changed defaults: %s", got)
	}
}
