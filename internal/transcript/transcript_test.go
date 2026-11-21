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

func TestPiShellStyle(t *testing.T) {
	entries := []ccmodel.Entry{
		{Type: "assistant", Message: &ccmodel.Message{Role: "assistant", Content: json.RawMessage(
			`[{"type":"tool_use","name":"bash","input":{"command":"go version"}}]`)}},
	}
	out := Render(entries, Options{Style: PiStyle})
	if !strings.Contains(out, "$ go version") || strings.Contains(out, "bash(") {
		t.Errorf("pi style render = %q, want bare $ cmd", out)
	}
}

func TestResultTailFold(t *testing.T) {
	var b strings.Builder
	tr := ccmodel.ContentBlock{Content: "l1\nl2\nl3\nl4\nl5\nl6\nl7"}
	renderResult(&b, "", tr, nil, CodexStyle, 0)
	out := b.String()
	for _, want := range []string{"l1", "l2", "… +3 lines", "l6", "l7"} {
		if !strings.Contains(out, want) {
			t.Errorf("tail fold missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "l4") {
		t.Errorf("tail fold shows folded line l4:\n%s", out)
	}
}

func TestHideResults(t *testing.T) {
	var b strings.Builder
	renderResult(&b, "", ccmodel.ContentBlock{Content: "secret"}, nil, AntigravityStyle, 0)
	if b.String() != "" {
		t.Errorf("HideResults rendered %q, want empty", b.String())
	}
}

func TestMdInline(t *testing.T) {
	if got := mdInline("run **Go 1.26** on `darwin`"); got != "run Go 1.26 on `darwin`" {
		t.Errorf("mdInline = %q", got)
	}
	if got := mdInline("a ** unpaired"); got != "a ** unpaired" {
		t.Errorf("mdInline(unpaired) = %q", got)
	}
}

func TestStyleFor(t *testing.T) {
	if StyleFor("codex").Shell != "Ran " {
		t.Error("StyleFor(codex) != CodexStyle")
	}
	if StyleFor("claude").User != "❯" || StyleFor("").User != "❯" {
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

func TestRenderQuestions(t *testing.T) {
	entries := []ccmodel.Entry{
		{Type: "assistant", Message: &ccmodel.Message{Role: "assistant", Content: json.RawMessage(
			`[{"type":"tool_use","name":"AskUserQuestion","input":{"questions":[
			  {"question":"Which path?","header":"Approach","options":[
			    {"label":"Fast (Recommended)"},{"label":"Careful"}]}]}}]`)}},
	}
	out := Render(entries, Options{})
	for _, want := range []string{"AskUserQuestion (1)", "[Approach]", "Which path?", "○ Fast (Recommended)", "○ Careful"} {
		if !strings.Contains(out, want) {
			t.Errorf("renderQuestions missing %q in:\n%s", want, out)
		}
	}
}

func TestResultSummary(t *testing.T) {
	if got := resultSummary(&ccmodel.ToolUseResult{File: &ccmodel.FileResult{NumLines: 41}}); got != "Read 41 lines" {
		t.Errorf("resultSummary(file) = %q", got)
	}
	if got := resultSummary(&ccmodel.ToolUseResult{NumFiles: 3}); got != "Found 3 files" {
		t.Errorf("resultSummary(files) = %q", got)
	}
	if got := resultSummary(nil); got != "" {
		t.Errorf("resultSummary(nil) = %q", got)
	}
}

func TestEmptyResult(t *testing.T) {
	var b strings.Builder
	renderResult(&b, "", ccmodel.ContentBlock{Content: "  "}, nil, ClaudeStyle, 0)
	if !strings.Contains(b.String(), "(No output)") {
		t.Errorf("empty result = %q, want (No output)", b.String())
	}
	b.Reset()
	renderResult(&b, "", ccmodel.ContentBlock{Content: ""}, nil, CodexStyle, 0)
	if b.String() != "" {
		t.Errorf("codex empty result = %q, want hidden", b.String())
	}
}

func TestMdHeadings(t *testing.T) {
	if got := mdInline("### Overview\nbody"); got != "Overview\nbody" {
		t.Errorf("mdInline(heading) = %q", got)
	}
	if got := mdInline("#!/bin/sh"); got != "#!/bin/sh" {
		t.Errorf("mdInline(shebang) = %q", got)
	}
}

func TestPrimaryArg(t *testing.T) {
	in := json.RawMessage(`{"replace_all":false,"file_path":"/a/b.md","old_string":"x"}`)
	if got := primaryArg(in); got != "/a/b.md" {
		t.Errorf("primaryArg = %q", got)
	}
	if got := primaryArg(json.RawMessage(`{"n":1}`)); got != "" {
		t.Errorf("primaryArg(none) = %q", got)
	}
}

func TestExploreItems(t *testing.T) {
	items, ok := exploreItems("ls cmd | head && sed -n '1,15p' go.mod")
	if !ok || len(items) != 2 || items[0] != "List cmd" || items[1] != "Read go.mod" {
		t.Errorf("exploreItems = %v, %v", items, ok)
	}
	if _, ok := exploreItems("rm -rf /"); ok {
		t.Error("exploreItems(rm) ok, want false")
	}
	if _, ok := exploreItems("go test ./..."); ok {
		t.Error("exploreItems(go test) ok, want false")
	}
}

func TestLineRange(t *testing.T) {
	in := json.RawMessage(`{"file_path":"/a/go.mod","limit":15}`)
	if got := lineRange(in, ClaudeStyle); got != " · lines 1-15" {
		t.Errorf("lineRange = %q", got)
	}
	if got := lineRange(in, PiStyle); got != ":1-15" {
		t.Errorf("lineRange(pi) = %q", got)
	}
	if got := lineRange(json.RawMessage(`{"file_path":"/a"}`), ClaudeStyle); got != "" {
		t.Errorf("lineRange(no limit) = %q", got)
	}
}

func TestColorCode(t *testing.T) {
	got := colorCode("see `go.mod` here")
	if strings.Contains(got, "`") || !strings.Contains(got, "go.mod") {
		t.Errorf("colorCode = %q", got)
	}
	if got := colorCode("no spans"); got != "no spans" {
		t.Errorf("colorCode(plain) = %q", got)
	}
}

func TestMdLinks(t *testing.T) {
	if got := mdInline("see [`go.mod`](file:///x/go.mod#L1) here"); got != "see `go.mod` here" {
		t.Errorf("mdInline(link) = %q", got)
	}
}
