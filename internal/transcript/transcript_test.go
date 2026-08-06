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

func TestRenderLongTailBlocks(t *testing.T) {
	entries := []ccmodel.Entry{
		{Type: "user", Message: &ccmodel.Message{Role: "user", Content: json.RawMessage(`[{"type":"image","data":"AA=="}]`)}},
		{Type: "assistant", AgentID: "reviewer", IsSidechain: true, Message: &ccmodel.Message{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","name":"Edit","input":{"file_path":"a.go","old_string":"old","new_string":"new"}},{"type":"image","data":"AA=="}]`)}},
		{Type: "user", Message: &ccmodel.Message{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"x","content":"patch failed","is_error":true}]`)}},
	}
	out := Render(entries, Options{Width: 80})
	for _, want := range []string{"[image]", "subagent reviewer", "Edit", "patch failed"} {
		if !strings.Contains(out, want) {
			t.Errorf("long-tail render missing %q in:\n%s", want, out)
		}
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

func TestExpand(t *testing.T) {
	entries := []ccmodel.Entry{{
		Type: "user",
		Message: &ccmodel.Message{Role: "user", Content: json.RawMessage(
			`[{"type":"tool_result","tool_use_id":"t1","content":"l1\nl2\nl3\nl4\nl5\nl6\nl7"}]`)},
	}}
	folded := Render(entries, Options{Style: CodexStyle})
	if !strings.Contains(folded, "… +3 lines") {
		t.Fatalf("folded render lost its fold marker:\n%s", folded)
	}
	full := Render(entries, Options{Style: CodexStyle, Expand: true})
	if strings.Contains(full, "+3 lines") {
		t.Errorf("expanded render still folds:\n%s", full)
	}
	for _, want := range []string{"l1", "l4", "l7"} {
		if !strings.Contains(full, want) {
			t.Errorf("expanded render missing %q:\n%s", want, full)
		}
	}
	// Antigravity hides result bodies entirely; expanding reveals them.
	hidden := Render(entries, Options{Style: AntigravityStyle})
	if strings.Contains(hidden, "l1") {
		t.Errorf("HideResults leaked a body:\n%s", hidden)
	}
	if shown := Render(entries, Options{Style: AntigravityStyle, Expand: true}); !strings.Contains(shown, "l7") {
		t.Errorf("expanded Antigravity render still hides bodies:\n%s", shown)
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
	// Bold markers pass through mdInline; boldSpans styles them post-wrap.
	if got := mdInline("run **Go 1.26** on `darwin`"); got != "run **Go 1.26** on `darwin`" {
		t.Errorf("mdInline = %q", got)
	}
}

func TestBoldSpans(t *testing.T) {
	got := boldSpans("run **Go 1.26** now")
	if strings.Contains(got, "**") || !strings.Contains(got, "Go 1.26") {
		t.Errorf("boldSpans = %q", got)
	}
	// A span the wrap split across lines loses its markers.
	if got := boldSpans("a **split\nspan** b"); strings.Contains(got, "**") {
		t.Errorf("boldSpans(dangling) = %q", got)
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
	// mdInline leaves headings for boldHeads, which styles them post-wrap.
	if got := mdInline("### Overview\nbody"); got != "### Overview\nbody" {
		t.Errorf("mdInline(heading) = %q", got)
	}
	if got := boldHeads("### Overview\nbody", false); !strings.Contains(got, "Overview") || strings.Contains(got, "#") {
		t.Errorf("boldHeads = %q, want hashes dropped", got)
	}
	if got := boldHeads("### Overview", true); !strings.Contains(got, "### Overview") {
		t.Errorf("boldHeads(keepHash) = %q, want hashes kept", got)
	}
	if got := boldHeads("#!/bin/sh", false); got != "#!/bin/sh" {
		t.Errorf("boldHeads(shebang) = %q", got)
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

func TestAnswersSummary(t *testing.T) {
	tur := &ccmodel.ToolUseResult{
		Questions: []ccmodel.AskedQuestion{{Question: "Snack?"}, {Question: "Drink?"}},
		Answers:   map[string]string{"Snack?": "Apple", "Drink?": "Water, Tea"},
	}
	got := resultSummary(tur)
	if got != "· Snack? → Apple\n· Drink? → Water, Tea" {
		t.Errorf("resultSummary(answers) = %q", got)
	}
}

func TestParseQuestionsAgy(t *testing.T) {
	in := json.RawMessage(`{"questions":"[{\"is_multi_select\":false,\"options\":[\"Go\",\"Python\"],\"question\":\"Which language?\"}]"}`)
	qs := parseQuestions(in)
	if len(qs) != 1 || qs[0].question != "Which language?" || len(qs[0].options) != 2 || qs[0].options[0] != "Go" {
		t.Errorf("parseQuestions(agy) = %+v", qs)
	}
}

func TestAPIErrorRender(t *testing.T) {
	entries := []ccmodel.Entry{
		{Type: "assistant", IsAPIErrorMessage: true, Message: &ccmodel.Message{Role: "assistant", Content: json.RawMessage(
			`[{"type":"text","text":"API Error: Credit balance is too low"}]`)}},
	}
	out := Render(entries, Options{})
	if !strings.Contains(out, "Credit balance is too low") {
		t.Errorf("API error missing from render:\n%s", out)
	}
}

func TestThinkingRender(t *testing.T) {
	entries := []ccmodel.Entry{
		{Type: "assistant", Message: &ccmodel.Message{Role: "assistant", Content: json.RawMessage(
			`[{"type":"thinking","thinking":"pondering the layout"}]`)}},
	}
	out := Render(entries, Options{Style: ClaudeStyle})
	if !strings.Contains(out, "∴") || !strings.Contains(out, "pondering the layout") {
		t.Errorf("claude thinking render = %q, want dim ∴ + text", out)
	}
	// Agy readers store thinking text in Text rather than Thinking.
	entries[0].Message.Content = json.RawMessage(`[{"type":"thinking","text":"agy thought"}]`)
	out = Render(entries, Options{Style: AntigravityStyle})
	if !strings.Contains(out, "agy thought") {
		t.Errorf("agy thinking render = %q, want text fallback", out)
	}
	// A style with no Thinking marker hides the block.
	hidden := ClaudeStyle
	hidden.Thinking = ""
	if out := Render(entries, Options{Style: hidden}); strings.Contains(out, "agy thought") {
		t.Errorf("hidden thinking rendered: %q", out)
	}
}
