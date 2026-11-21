package collector

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tmc/cc"
)

func TestExtractSkillsCodexAvailable(t *testing.T) {
	entries := []cc.Entry{{
		Timestamp: time.Unix(10, 0),
		Message: textMessage("developer", `## Skills
A skill is a set of local instructions.
### Available skills
- imagegen: Generate images. (file: /Users/example/.codex/skills/.system/imagegen/SKILL.md)
- browser-use:browser: Browser automation. (file: /Users/example/.codex/plugins/browser-use/skills/browser/SKILL.md)`),
	}}

	skills := ExtractSkills(entries, "codex")
	if len(skills) != 2 {
		t.Fatalf("len(skills) = %d, want 2", len(skills))
	}
	if skills[0].Name != "browser-use:browser" || skills[0].Kind != "available" || skills[0].Path == "" {
		t.Fatalf("first skill = %+v, want browser-use:browser available with path", skills[0])
	}
	if skills[1].Name != "imagegen" || skills[1].Kind != "available" {
		t.Fatalf("second skill = %+v, want imagegen available", skills[1])
	}
}

func TestExtractSkillsClaudeSelectedAndLoaded(t *testing.T) {
	readInput, _ := json.Marshal(map[string]string{"file_path": "/Users/example/.claude/skills/nlm/SKILL.md"})
	skillInput, _ := json.Marshal(map[string]string{"skill": "nlm"})
	entries := []cc.Entry{
		{
			Timestamp: time.Unix(20, 0),
			Message: toolMessage("assistant", cc.ContentBlock{
				Type:  "tool_use",
				Name:  "Skill",
				Input: skillInput,
			}),
		},
		{
			Timestamp: time.Unix(21, 0),
			Message: toolMessage("assistant", cc.ContentBlock{
				Type:  "tool_use",
				Name:  "Read",
				Input: readInput,
			}),
		},
	}

	skills := ExtractSkills(entries, "claude-code")
	byKind := map[string]ccSkill{}
	for _, s := range skills {
		byKind[s.Kind] = ccSkill{s.Name, s.Path, s.Count}
	}
	if got := byKind["selected"]; got.name != "nlm" || got.count != 1 {
		t.Fatalf("selected = %+v, want nlm count 1", got)
	}
	if got := byKind["loaded"]; got.name != "nlm" || got.path == "" {
		t.Fatalf("loaded = %+v, want nlm with path", got)
	}
}

type ccSkill struct {
	name  string
	path  string
	count int
}

func textMessage(role, text string) *cc.Message {
	raw, _ := json.Marshal([]cc.ContentBlock{{Type: "text", Text: text}})
	return &cc.Message{Role: role, Content: raw}
}

func toolMessage(role string, blocks ...cc.ContentBlock) *cc.Message {
	raw, _ := json.Marshal(blocks)
	return &cc.Message{Role: role, Content: raw}
}

// The transcripts below copy the shape of real session lines and go through
// the session reader, so the test fails if either the extractor or the reader
// stops agreeing with what harnesses write.

const claudeSlashSkillJSONL = `{"type":"user","uuid":"u1","timestamp":"2026-09-22T06:12:16.461Z","sessionId":"s1","message":{"role":"user","content":"<command-message>goal</command-message>\n<command-name>/goal</command-name>\n<command-args>prep a release</command-args>"}}
{"type":"user","uuid":"u2","parentUuid":"u1","isMeta":true,"timestamp":"2026-09-22T06:12:16.462Z","sessionId":"s1","message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /Users/example/.claude/skills/goal\n\n# Goal\n\n- ` + "`<objective>`" + `: the goal text"}]}}
`

const claudeSkillToolJSONL = `{"type":"assistant","uuid":"a1","timestamp":"2026-09-22T06:12:16.000Z","sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Skill","input":{"skill":"anthropic-skills:deep-research"}}]}}
{"type":"user","uuid":"u1","parentUuid":"a1","timestamp":"2026-09-22T06:12:17.000Z","sessionId":"s1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Launching skill: anthropic-skills:deep-research"}]}}
{"type":"user","uuid":"u2","parentUuid":"u1","isMeta":true,"sourceToolUseID":"toolu_1","timestamp":"2026-09-22T06:12:17.001Z","sessionId":"s1","message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: /Users/example/.claude/skills/synced/0000_abcd/deep-research\n\n# Coordinate"}]}}
`

const codexSkillJSONL = `{"timestamp":"2026-10-01T03:28:40.000Z","type":"session_meta","payload":{"id":"01a0f538-920e-7641-921c-90f442d626e3","cwd":"/tmp/w","originator":"codex_cli_rs"}}
{"timestamp":"2026-10-01T03:28:43.393Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<skill>\n<name>go-team-codebase-review</name>\n<path>/Users/example/.codex/skills/go-team-codebase-review/SKILL.md</path>\n---\nname: go-team-codebase-review\n"}]}}
{"timestamp":"2026-10-01T03:28:44.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n 1,200p /Users/example/.codex/skills/nlm/SKILL.md\"}","call_id":"c1"}}
{"timestamp":"2026-10-01T03:28:45.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"For <skill> only, review <dimension> against the draft."}]}}
`

func TestExtractSkillsFromTranscripts(t *testing.T) {
	type use struct{ name, kind, path string }
	tests := []struct {
		name  string
		jsonl string
		want  []use
	}{
		{"claude slash command", claudeSlashSkillJSONL, []use{
			{"goal", "loaded", "/Users/example/.claude/skills/goal/SKILL.md"},
			{"goal", "selected", "/Users/example/.claude/skills/goal/SKILL.md"},
		}},
		{"claude skill tool", claudeSkillToolJSONL, []use{
			{"anthropic-skills:deep-research", "selected", ""},
			{"deep-research", "loaded", "/Users/example/.claude/skills/synced/0000_abcd/deep-research/SKILL.md"},
		}},
		{"codex $skill and shell read", codexSkillJSONL, []use{
			{"go-team-codebase-review", "selected", "/Users/example/.codex/skills/go-team-codebase-review/SKILL.md"},
			{"nlm", "loaded", "/Users/example/.codex/skills/nlm/SKILL.md"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, err := cc.ReadAll(context.Background(), strings.NewReader(tt.jsonl))
			if err != nil {
				t.Fatal(err)
			}
			var got []use
			for _, s := range ExtractSkills(entries, "test") {
				got = append(got, use{s.Name, s.Kind, s.Path})
			}
			sort.Slice(got, func(i, j int) bool { return got[i].name+got[i].kind < got[j].name+got[j].kind })
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("skills = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSkillSetToolNames(t *testing.T) {
	tests := []struct {
		tool, input string
		wantName    string
		wantKind    string
	}{
		{"skill", `{"name":"manimce-best-practices"}`, "manimce-best-practices", "selected"},
		{"activate_skill", `{"name":"pdf"}`, "pdf", "selected"},
		{"read", `{"path":"/Users/example/.pi/agent/skills/it2/SKILL.md"}`, "it2", "loaded"},
		{"read", `{"filePath":"/w/.opencode/skills/review/SKILL.md"}`, "review", "loaded"},
		{"read_file", `{"absolute_path":"/Users/example/.gemini/skills/docs/SKILL.md"}`, "docs", "loaded"},
		{"bash", `{"command":"cat ~/.pi/agent/skills/goal/SKILL.md"}`, "goal", "loaded"},
		{"read", `{"path":"/w/README.md"}`, "", ""},
		{"bash", `{"command":"for d in ~/.claude/skills/*; do cat $d/SKILL.md; done"}`, "", ""},
	}
	for _, tt := range tests {
		var set skillSet
		set.addToolUse(tt.tool, json.RawMessage(tt.input), time.Unix(1, 0))
		got := set.list("test")
		if tt.wantName == "" {
			if len(got) != 0 {
				t.Errorf("%s %s = %+v, want none", tt.tool, tt.input, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Name != tt.wantName || got[0].Kind != tt.wantKind {
			t.Errorf("%s %s = %+v, want %s %s", tt.tool, tt.input, got, tt.wantName, tt.wantKind)
		}
	}
}
