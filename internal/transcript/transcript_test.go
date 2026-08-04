package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tmc/cc/internal/ccmodel"
)

func TestRender(t *testing.T) {
	mkmsg := func(role, content string) *ccmodel.Message {
		return &ccmodel.Message{Role: role, Content: json.RawMessage(content)}
	}
	entries := []ccmodel.Entry{
		{Type: "user", Message: mkmsg("user", `"hello there"`)},
		{Type: "assistant", Message: mkmsg("assistant",
			`[{"type":"text","text":"hi back"},{"type":"tool_use","name":"Bash","input":{"command":"ls -l"}}]`)},
		{Type: "user", Message: mkmsg("user",
			`[{"type":"tool_result","tool_use_id":"x","content":"total 0","is_error":true}]`)},
		{Type: "user", IsMeta: true, Message: mkmsg("user", `"meta noise"`)},
	}
	out := Render(entries, Options{})
	for _, want := range []string{"hello there", "hi back", "Bash", "ls -l", "total 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "meta noise") {
		t.Error("Render included IsMeta entry")
	}
}
