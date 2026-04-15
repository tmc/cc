package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc"
)

func TestRunMarkdown(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the failing test"}]}}`,
		`{"timestamp":"2026-04-10T21:03:55Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"I am checking the tests."}]}}`,
		`{"timestamp":"2026-04-10T21:03:56Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":"{\"cmd\":\"go test ./...\"}"}}`,
		`{"timestamp":"2026-04-10T21:03:57Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"{\"output\":\"ok\",\"metadata\":{\"exit_code\":0}}"}}`,
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"# repo",
		"- Session: `sid-123`",
		"## User",
		"> fix the failing test",
		"## Assistant (commentary)",
		"### Tool: `Bash`",
		"```sh",
		"go test ./...",
		"## Tool Result",
		"```text",
		"ok",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown output missing %q\n%s", want, got)
		}
	}
}

func TestRunMarkdownQuotesMessageBodies(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		"{\"timestamp\":\"2026-04-10T21:03:54Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"# heading\\n- item\\n```go\\nfmt.Println(1)\\n```\"}]}}",
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"> # heading",
		"> - item",
		"> ```go",
		"> fmt.Println(1)",
		"> ```",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("quoted markdown body missing %q\n%s", want, got)
		}
	}
}

func TestRunMarkdownInlinesLocalImage(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "dot.png")
	writePNGFile(t, imgPath, color.RGBA{R: 255, A: 255})
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		fmt.Sprintf("{\"timestamp\":\"2026-04-10T21:03:54Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"path\":%q}]}}", imgPath),
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "![image-1](data:") {
		t.Fatalf("markdown output missing inline image:\n%s", got)
	}
}

func TestRunMarkdownInlinesLocalImageThroughRedactor(t *testing.T) {
	tmp := t.TempDir()
	imgPath := filepath.Join(tmp, "dot.png")
	writePNGFile(t, imgPath, color.RGBA{R: 255, A: 255})
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		fmt.Sprintf("{\"timestamp\":\"2026-04-10T21:03:54Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"path\":%q}]}}", imgPath),
	}, "\n")+"\n")

	old := redactImageDataFunc
	redactImageDataFunc = func(data []byte) ([]byte, error) {
		return solidPNG(color.RGBA{G: 255, A: 255}), nil
	}
	defer func() { redactImageDataFunc = old }()

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), base64.StdEncoding.EncodeToString(solidPNG(color.RGBA{G: 255, A: 255}))) {
		t.Fatalf("markdown output missing redacted image payload:\n%s", out.String())
	}
}

func TestRunMarkdownToolResultRendersInlineImage(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"[{\"type\":\"input_text\",\"text\":\"saved screenshot\"},{\"type\":\"image\",\"image_url\":\"data:image/png;base64,AAAA\"}]"}}`,
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "saved screenshot") {
		t.Fatalf("markdown output missing tool result text:\n%s", got)
	}
	if !strings.Contains(got, "![tool-result-image-2](data:image/png;base64,AAAA)") {
		t.Fatalf("markdown output missing tool result image:\n%s", got)
	}
}

func TestRunMarkdownToolResultImageUsesRedactor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	orig := solidPNG(color.RGBA{R: 255, A: 255})
	orig64 := base64.StdEncoding.EncodeToString(orig)
	want := solidPNG(color.RGBA{B: 255, A: 255})
	want64 := base64.StdEncoding.EncodeToString(want)
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		fmt.Sprintf("{\"timestamp\":\"2026-04-10T21:03:54Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"call-1\",\"output\":\"[{\\\"type\\\":\\\"image\\\",\\\"image_url\\\":\\\"data:image/png;base64,%s\\\"}]\"}}", orig64),
	}, "\n")+"\n")

	old := redactImageDataFunc
	redactImageDataFunc = func(data []byte) ([]byte, error) { return want, nil }
	defer func() { redactImageDataFunc = old }()

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if strings.Contains(got, orig64) {
		t.Fatalf("tool result image was not redacted:\n%s", got)
	}
	if !strings.Contains(got, want64) {
		t.Fatalf("tool result image missing redacted payload:\n%s", got)
	}
}

func TestRunMarkdownRedactsMessageSecrets(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"use pk_test_12345 and sk_test_67890"}]}}`,
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "pk_test_12345") || strings.Contains(got, "sk_test_67890") {
		t.Fatalf("markdown output leaked secret:\n%s", got)
	}
	for _, want := range []string{"pk_test_XXXXX...", "sk_test_XXXXX..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("markdown output missing redaction %q:\n%s", want, got)
		}
	}
}

func TestToolCommandRedactsSecrets(t *testing.T) {
	block := cc.ContentBlock{
		Name:  "Bash",
		Input: jsonRaw(`{"cmd":"echo sk_test_67890 pk_test_12345"}`),
	}
	if got := toolCommand(block); got != "echo sk_test_XXXXX... pk_test_XXXXX..." {
		t.Fatalf("toolCommand redaction = %q", got)
	}
}

func TestToolResultTextRedactsSecrets(t *testing.T) {
	resetFlagsForTest()
	block := cc.ContentBlock{
		Content: "secret: sk_test_67890\npublishable: pk_test_12345",
	}
	got := toolResultText(block)
	if strings.Contains(got, "sk_test_67890") || strings.Contains(got, "pk_test_12345") {
		t.Fatalf("tool result leaked secret: %q", got)
	}
	for _, want := range []string{"sk_test_XXXXX...", "pk_test_XXXXX..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("tool result missing redaction %q: %q", want, got)
		}
	}
}

func TestRunTextSkipsMetaByDefault(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work/repo"}]}}`,
		`{"timestamp":"2026-04-10T21:03:55Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"real request"}]}}`,
	}, "\n")+"\n")

	resetFlagsForTest()

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "AGENTS.md") {
		t.Fatalf("text output unexpectedly included meta preamble:\n%s", got)
	}
	if !strings.Contains(got, "real request") {
		t.Fatalf("text output missing user text:\n%s", got)
	}
}

func TestRunMarkdownPublishCleanupSummarizesTools(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking"}]}}`,
		`{"timestamp":"2026-04-10T21:03:56Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":"{\"cmd\":\"rg --files .\"}"}}`,
		`{"timestamp":"2026-04-10T21:03:57Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"{\"output\":\"line1\\nline2\\nline3\\nline4\",\"metadata\":{\"exit_code\":0}}"}}`,
	}, "\n")+"\n")

	resetFlagsForTest()
	*formatFlag = "markdown"
	*cleanupFlag = "publish"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "- Tool `Bash`: `rg --files .`") {
		t.Fatalf("missing summarized tool line:\n%s", got)
	}
	if strings.Contains(got, "### Tool: `Bash`") {
		t.Fatalf("unexpected full tool block in publish cleanup:\n%s", got)
	}
	if strings.Contains(got, "```text") {
		t.Fatalf("unexpected full tool result block in publish cleanup:\n%s", got)
	}
}

func TestRunOmitCommentaryAndToolResults(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	writeFile(t, path, strings.Join([]string{
		`{"timestamp":"2026-04-10T21:03:53Z","type":"session_meta","payload":{"id":"sid-123","cwd":"/work/repo","cli_version":"0.1.0","originator":"Codex Desktop","source":"vscode"}}`,
		`{"timestamp":"2026-04-10T21:03:54Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"checking"}]}}`,
		`{"timestamp":"2026-04-10T21:03:56Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":"{\"cmd\":\"go test ./...\"}"}}`,
		`{"timestamp":"2026-04-10T21:03:57Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"{\"output\":\"ok\",\"metadata\":{\"exit_code\":0}}"}}`,
	}, "\n")+"\n")

	resetFlagsForTest()
	*commentaryFlag = false
	*toolResultsFlag = "omit"

	var out bytes.Buffer
	if err := run(&out, []string{path}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "checking") {
		t.Fatalf("unexpected commentary in output:\n%s", got)
	}
	if strings.Contains(got, "[tool result]") {
		t.Fatalf("unexpected tool result in output:\n%s", got)
	}
}

func TestToolCommandAcceptsCmdField(t *testing.T) {
	block := cc.ContentBlock{
		Name:  "Bash",
		Input: jsonRaw(`{"cmd":"rg --files ."}`),
	}
	if got := toolCommand(block); got != "rg --files ." {
		t.Fatalf("toolCommand(cmd) = %q, want %q", got, "rg --files .")
	}
}

func TestSummarizeToolResultPatchedFiles(t *testing.T) {
	body := "Success. Updated the following files:\nM /tmp/a.go\nA /tmp/b.go\n"
	if got := summarizeToolResult(body); got != "Updated files: /tmp/a.go, /tmp/b.go" {
		t.Fatalf("summarizeToolResult = %q", got)
	}
}

func TestNoiseEntryIsDropped(t *testing.T) {
	resetFlagsForTest()
	var out bytes.Buffer
	err := runEntries(&out, "stdin", []cc.Entry{
		{Type: "reasoning", Content: "encrypted"},
		{Type: "event_msg", Content: "token_count"},
		{Type: "user", Message: &cc.Message{Role: "user", Content: jsonRaw(`[{"type":"text","text":"hello"}]`)}},
	})
	if err != nil {
		t.Fatalf("runEntries: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "encrypted") || strings.Contains(got, "token_count") {
		t.Fatalf("noise leaked into output:\n%s", got)
	}
	if !strings.Contains(got, "hello") {
		t.Fatalf("user message missing:\n%s", got)
	}
}

func runEntries(w *bytes.Buffer, source string, entries []cc.Entry) error {
	switch *formatFlag {
	case "text":
		return writeText(w, source, entries)
	case "markdown", "md":
		return writeMarkdown(w, source, entries)
	default:
		return fmt.Errorf("unsupported test format %q", *formatFlag)
	}
}

func resetFlagsForTest() {
	*formatFlag = "text"
	*titleFlag = ""
	*includeMeta = false
	*includeStamp = false
	*maxBytesFlag = 4000
	*cleanupFlag = "none"
	*commentaryFlag = true
	*toolsFlag = "full"
	*toolResultsFlag = "full"
	*inlineImagesFlag = true
	*redactImagesFlag = true
	*redactFlag = true
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func jsonRaw(s string) []byte {
	return []byte(s)
}

func writePNGFile(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	if err := os.WriteFile(path, solidPNG(c), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func solidPNG(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, c)
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
