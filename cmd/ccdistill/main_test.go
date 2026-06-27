package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc/ccdistill"
)

// sampleSession is a minimal two-turn session with one tool call, written in
// the Claude JSONL shape the cc reader understands.
const sampleSession = `{"type":"user","sessionId":"sess-1","uuid":"u1","cwd":"/tmp/app","gitBranch":"main","message":{"role":"user","content":"add a -v flag"}}
{"type":"assistant","sessionId":"sess-1","uuid":"a1","message":{"role":"assistant","model":"claude-opus-4","content":[{"type":"text","text":"I'll read main.go."},{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file_path":"main.go"}}]}}
{"type":"user","sessionId":"sess-1","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"package main"}]}}
{"type":"assistant","sessionId":"sess-1","uuid":"a2","message":{"role":"assistant","model":"claude-opus-4","content":[{"type":"text","text":"Done."}]}}
`

func TestParseFormat(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ccdistill.Format
		wantErr bool
	}{
		{"anthropic", "anthropic", ccdistill.FormatAnthropic, false},
		{"openai", "openai", ccdistill.FormatOpenAI, false},
		{"sharegpt", "sharegpt", ccdistill.FormatShareGPT, false},
		{"unknown", "yaml", "", true},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFormat(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseFormat(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
			if err != nil {
				if _, ok := err.(usageError); !ok {
					t.Errorf("parseFormat(%q) error is not usageError: %T", tt.in, err)
				}
				return
			}
			if got != tt.want {
				t.Errorf("parseFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSelectInputsStdinExclusive(t *testing.T) {
	_, _, err := selectInputs(context.Background(), []string{"-", "extra.jsonl"})
	if err == nil {
		t.Fatal("expected error combining - with other args")
	}
	if _, ok := err.(usageError); !ok {
		t.Errorf("error is not usageError: %T", err)
	}

	_, stdin, err := selectInputs(context.Background(), []string{"-"})
	if err != nil {
		t.Fatalf("selectInputs([-]) err=%v", err)
	}
	if !stdin {
		t.Error("selectInputs([-]) did not signal stdin")
	}
}

func TestSelectInputsExplicitAndDir(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(a, []byte(sampleSession), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(sampleSession), 0o644); err != nil {
		t.Fatal(err)
	}

	files, stdin, err := selectInputs(context.Background(), []string{dir})
	if err != nil {
		t.Fatalf("selectInputs(dir) err=%v", err)
	}
	if stdin {
		t.Error("dir arg should not signal stdin")
	}
	if len(files) != 2 {
		t.Errorf("walked %d files, want 2", len(files))
	}

	files, _, err = selectInputs(context.Background(), []string{a})
	if err != nil {
		t.Fatalf("selectInputs(file) err=%v", err)
	}
	if len(files) != 1 || files[0] != a {
		t.Errorf("explicit file = %v, want [%s]", files, a)
	}
}

func TestAssignTrainDeterministic(t *testing.T) {
	// Same fingerprint always lands the same side.
	const fp = "abc123"
	first := assignTrain(fp, 0.9)
	for i := 0; i < 100; i++ {
		if assignTrain(fp, 0.9) != first {
			t.Fatal("assignTrain not deterministic for a fixed fingerprint")
		}
	}
	// ratio 1 => always train; ratio near 0 => almost never train.
	if !assignTrain(fp, 1) {
		t.Error("ratio 1 should always assign train")
	}
}

func TestAssignTrainRatioBalance(t *testing.T) {
	train := 0
	const n = 2000
	for i := 0; i < n; i++ {
		if assignTrain(string(rune('a'+i%26))+itoa(i), 0.8) {
			train++
		}
	}
	frac := float64(train) / float64(n)
	if frac < 0.74 || frac > 0.86 {
		t.Errorf("train fraction = %.3f, want ~0.80", frac)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// buildBinary compiles the CLI once for end-to-end subprocess tests.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ccdistill")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestEndToEndStdin(t *testing.T) {
	bin := buildBinary(t)
	for _, format := range []string{"anthropic", "openai", "sharegpt"} {
		t.Run(format, func(t *testing.T) {
			cmd := exec.Command(bin, "-format", format, "-")
			cmd.Stdin = strings.NewReader(sampleSession)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			lines := nonEmptyLines(out)
			if len(lines) != 1 {
				t.Fatalf("got %d records, want 1: %q", len(lines), out)
			}
			var rec map[string]json.RawMessage
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatalf("record is not JSON: %v\n%s", err, lines[0])
			}
		})
	}
}

func TestEndToEndUnknownFormatExit2(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-format", "yaml", "-")
	cmd.Stdin = strings.NewReader(sampleSession)
	err := cmd.Run()
	if code := exitCode(err); code != 2 {
		t.Fatalf("unknown format exit = %d, want 2", code)
	}
}

func TestEndToEndSplitOutOfRangeExit2(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-split", "1.5", "-")
	cmd.Stdin = strings.NewReader(sampleSession)
	if code := exitCode(cmd.Run()); code != 2 {
		t.Fatalf("bad split exit = %d, want 2", code)
	}
}

func TestEndToEndStdinWithExtraArgExit2(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-", "other.jsonl")
	cmd.Stdin = strings.NewReader(sampleSession)
	if code := exitCode(cmd.Run()); code != 2 {
		t.Fatalf(`"-" plus extra arg exit = %d, want 2`, code)
	}
}

func TestEndToEndSplitFiles(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	sess := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(sess, []byte(sampleSession), 0o644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "out")
	cmd := exec.Command(bin, "-split", "0.9", "-out", outDir, sess)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	for _, name := range []string{"train.jsonl", "valid.jsonl"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("missing split file %s: %v", name, err)
		}
	}
}

func TestEndToEndOutputFile(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	sess := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(sess, []byte(sampleSession), 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "sft.jsonl")
	cmd := exec.Command(bin, "-o", outFile, sess)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonEmptyLines(data)) != 1 {
		t.Errorf("output file has %d records, want 1", len(nonEmptyLines(data)))
	}
}

func TestEndToEndRedactionDefault(t *testing.T) {
	bin := buildBinary(t)
	// A session whose tool result leaks an email; redaction is on by default.
	leaky := `{"type":"user","sessionId":"s","uuid":"u1","message":{"role":"user","content":"who am i"}}
{"type":"assistant","sessionId":"s","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"whoami"}}]}}
{"type":"user","sessionId":"s","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"contact alice@example.com"}]}}
`
	cmd := exec.Command(bin, "-")
	cmd.Stdin = strings.NewReader(leaky)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(string(out), "alice@example.com") {
		t.Error("email not redacted by default")
	}
	if !strings.Contains(string(out), "[REDACTED_EMAIL]") {
		t.Error("redaction token missing")
	}
}

func nonEmptyLines(b []byte) []string {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, sc.Text())
		}
	}
	return lines
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	return -1
}
