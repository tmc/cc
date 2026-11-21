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

func TestWrap(t *testing.T) {
	got := wrap("aaa bbb ccc ddd", 7, "  ")
	want := "aaa bbb\n  ccc\n  ddd"
	if got != want {
		t.Errorf("wrap = %q, want %q", got, want)
	}
	if got := wrap("short", 80, "  "); got != "short" {
		t.Errorf("wrap(short) = %q", got)
	}
}

func TestCodexCommand(t *testing.T) {
	input := json.RawMessage(`{"input":"const r = await tools.exec_command({\"cmd\":\"go version && ls\",\"timeout\":10})"}`)
	if got, want := codexCommand(input), "go version && ls"; got != want {
		t.Errorf("codexCommand = %q, want %q", got, want)
	}
	if got := codexCommand(json.RawMessage(`{"command":"ls"}`)); got != "" {
		t.Errorf("codexCommand(non-exec) = %q, want empty", got)
	}
}

func TestResultText(t *testing.T) {
	tr := ccmodel.ContentBlock{Content: `[{"text":"line one","type":"input_text"}]`}
	if got := resultText(tr); got != "line one" {
		t.Errorf("resultText = %q, want %q", got, "line one")
	}
	tr = ccmodel.ContentBlock{Content: "plain"}
	if got := resultText(tr); got != "plain" {
		t.Errorf("resultText = %q, want plain", got)
	}
}

func TestStripExecPreamble(t *testing.T) {
	in := "Script completed\nWall time 0.2 seconds\nOutput:\ngo version go1.25.3\nfile.go"
	if got, want := stripExecPreamble(in), "go version go1.25.3\nfile.go"; got != want {
		t.Errorf("stripExecPreamble = %q, want %q", got, want)
	}
	if got := stripExecPreamble("plain output"); got != "plain output" {
		t.Errorf("stripExecPreamble(plain) = %q", got)
	}
}

func TestStyleFor(t *testing.T) {
	if StyleFor("codex") != CodexStyle {
		t.Error("StyleFor(codex) != CodexStyle")
	}
	if StyleFor("claude") != ClaudeStyle || StyleFor("") != ClaudeStyle {
		t.Error("StyleFor default != ClaudeStyle")
	}
}

func TestUserText(t *testing.T) {
	in := "<USER_REQUEST> do the thing </USER_REQUEST> <ADDITIONAL_METADATA> noise </ADDITIONAL_METADATA>"
	if got := userText(in); got != "do the thing" {
		t.Errorf("userText = %q, want %q", got, "do the thing")
	}
	if got := userText("plain"); got != "plain" {
		t.Errorf("userText(plain) = %q", got)
	}
}

func TestAgyDetail(t *testing.T) {
	in := json.RawMessage(`{"CommandLine":"\"go version\"","Cwd":"\"/x\"","toolAction":"\"Running\""}`)
	if got := agyDetail(in); got != "go version" {
		t.Errorf("agyDetail = %q, want %q", got, "go version")
	}
	in = json.RawMessage(`{"toolSummary":"\"Read doc.go\""}`)
	if got := agyDetail(in); got != "Read doc.go" {
		t.Errorf("agyDetail = %q, want %q", got, "Read doc.go")
	}
	if got := agyDetail(json.RawMessage(`{"other":1}`)); got != "" {
		t.Errorf("agyDetail(no keys) = %q, want empty", got)
	}
}
