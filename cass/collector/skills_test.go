package collector

import (
	"encoding/json"
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
- imagegen: Generate images. (file: /Users/tmc/.codex/skills/.system/imagegen/SKILL.md)
- browser-use:browser: Browser automation. (file: /Users/tmc/.codex/plugins/browser-use/skills/browser/SKILL.md)`),
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
	readInput, _ := json.Marshal(map[string]string{"file_path": "/Users/tmc/.claude/skills/nlm/SKILL.md"})
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
