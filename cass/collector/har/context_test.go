package har

import (
	"encoding/json"
	"testing"
)

func TestParseContextBreakdownSkillNames(t *testing.T) {
	body := map[string]any{
		"model": "claude-opus-4-7",
		"system": []map[string]string{
			{
				"type": "text",
				"text": "### Available skills\n- nlm: Notebook guidance. (file: /Users/tmc/.claude/skills/nlm/SKILL.md)",
			},
			{
				"type": "text",
				"text": "## Skill: notebooklm-assisted-prompting\nUse NotebookLM as advisor.",
			},
		},
		"tools": []map[string]string{
			{"name": "Bash"},
			{"name": "skill-provided-tool"},
		},
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	}
	raw, _ := json.Marshal(body)

	bd := ParseContextBreakdown(string(raw))
	want := []string{"nlm", "notebooklm-assisted-prompting", "skill-provided-tool"}
	if len(bd.SkillNames) != len(want) {
		t.Fatalf("SkillNames = %#v, want %#v", bd.SkillNames, want)
	}
	for i := range want {
		if bd.SkillNames[i] != want[i] {
			t.Fatalf("SkillNames = %#v, want %#v", bd.SkillNames, want)
		}
	}
	if bd.SkillToolBytes == 0 {
		t.Fatalf("SkillToolBytes = 0, want nonzero")
	}
}
