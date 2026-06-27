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

func TestParseHarnessFilter(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantSet map[string]bool
		wantErr bool
	}{
		{name: "empty disables filtering", in: "", wantSet: nil},
		{name: "whitespace only disables filtering", in: "  ", wantSet: nil},
		{name: "single name", in: "claude", wantSet: map[string]bool{"claude": true}},
		{
			name:    "multiple names with spaces",
			in:      "claude, codex ,gemini",
			wantSet: map[string]bool{"claude": true, "codex": true, "gemini": true},
		},
		{name: "all six valid names", in: "claude,codex,gemini,opencode,pi,antigravity",
			wantSet: map[string]bool{
				"claude": true, "codex": true, "gemini": true,
				"opencode": true, "pi": true, "antigravity": true,
			}},
		{name: "empty tokens skipped", in: "claude,,codex", wantSet: map[string]bool{"claude": true, "codex": true}},
		{name: "only commas disables filtering", in: ",,", wantSet: nil},
		{name: "unknown name errors", in: "claude,bogus", wantErr: true},
		{name: "unknown sentinel rejected", in: "unknown", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHarnessFilter(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseHarnessFilter(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
			}
			if err != nil {
				if _, ok := err.(usageError); !ok {
					t.Errorf("parseHarnessFilter(%q) error is not usageError: %T", tt.in, err)
				}
				return
			}
			if len(got) != len(tt.wantSet) {
				t.Fatalf("parseHarnessFilter(%q) = %v, want %v", tt.in, got, tt.wantSet)
			}
			for k := range tt.wantSet {
				if !got[k] {
					t.Errorf("parseHarnessFilter(%q) missing %q", tt.in, k)
				}
			}
		})
	}
}

// TestFilterByHarness verifies discovered files are kept only when their
// classified harness is in the allowlist. It builds real on-disk layouts under
// temporary homes so cc.Harness classifies them deterministically.
func TestFilterByHarness(t *testing.T) {
	base := t.TempDir()
	claudeHome := filepath.Join(base, "claude")
	geminiHome := filepath.Join(base, "gemini")
	codexHome := filepath.Join(base, "codex")
	t.Setenv("CLAUDE_HOME", claudeHome)
	t.Setenv("GEMINI_HOME", geminiHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("ANTIGRAVITY_HOME", filepath.Join(geminiHome, "antigravity"))
	t.Setenv("OPENCODE_HOME", filepath.Join(base, "opencode"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(base, "pi", "agent"))

	claudeFile := filepath.Join(claudeHome, "projects", "-p", "a.jsonl")
	codexFile := filepath.Join(codexHome, "sessions", "2026", "r.jsonl")
	geminiFile := filepath.Join(geminiHome, "projects", "-p", "g.jsonl")
	loose := filepath.Join(base, "elsewhere", "x.jsonl") // classifies as "unknown"
	files := []string{claudeFile, codexFile, geminiFile, loose}

	kept := filterByHarness(files, map[string]bool{"claude": true, "codex": true})
	want := []string{claudeFile, codexFile}
	if len(kept) != len(want) {
		t.Fatalf("filterByHarness kept %v, want %v", kept, want)
	}
	for i, w := range want {
		if kept[i] != w {
			t.Errorf("kept[%d] = %q, want %q", i, kept[i], w)
		}
	}
}

func TestSelectInputsStdinExclusive(t *testing.T) {
	_, _, err := selectInputs(context.Background(), []string{"-", "extra.jsonl"}, nil)
	if err == nil {
		t.Fatal("expected error combining - with other args")
	}
	if _, ok := err.(usageError); !ok {
		t.Errorf("error is not usageError: %T", err)
	}

	_, stdin, err := selectInputs(context.Background(), []string{"-"}, nil)
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

	files, stdin, err := selectInputs(context.Background(), []string{dir}, nil)
	if err != nil {
		t.Fatalf("selectInputs(dir) err=%v", err)
	}
	if stdin {
		t.Error("dir arg should not signal stdin")
	}
	if len(files) != 2 {
		t.Errorf("walked %d files, want 2", len(files))
	}

	files, _, err = selectInputs(context.Background(), []string{a}, nil)
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
	for range 100 {
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
	for i := range n {
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

func TestEndToEndUnknownHarnessExit2(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-harness", "bogus", "-")
	cmd.Stdin = strings.NewReader(sampleSession)
	if code := exitCode(cmd.Run()); code != 2 {
		t.Fatalf("unknown harness exit = %d, want 2", code)
	}
}

// TestEndToEndStdinNoHarnessKey confirms that a stdin run carries no harness key:
// identity is derived from the on-disk path, which stdin lacks, so process
// suppresses it for the "-" source rather than emitting the noise "unknown".
func TestEndToEndStdinNoHarnessKey(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "-")
	cmd.Stdin = strings.NewReader(sampleSession)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(string(out), "harness") {
		t.Errorf("stdin record leaked a harness key: %s", out)
	}
}

// TestEndToEndExplicitFileHarness confirms an explicit file under a known harness
// home is classified and its harness recorded in the encoded record. The file
// lives under a temporary CLAUDE_HOME/projects so cc.Harness returns "claude".
func TestEndToEndExplicitFileHarness(t *testing.T) {
	bin := buildBinary(t)
	home := t.TempDir()
	sess := filepath.Join(home, "projects", "-p", "s.jsonl")
	if err := os.MkdirAll(filepath.Dir(sess), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sess, []byte(sampleSession), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, sess)
	cmd.Env = append(os.Environ(), "CLAUDE_HOME="+home)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	lines := nonEmptyLines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d records, want 1: %q", len(lines), out)
	}
	var rec struct {
		Meta struct {
			Harness string `json:"harness"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("record is not JSON: %v\n%s", err, lines[0])
	}
	if rec.Meta.Harness != "claude" {
		t.Errorf("meta.harness = %q, want claude", rec.Meta.Harness)
	}
}

// TestEndToEndDropNoiseDefault confirms the CLI drops a leading slash-command
// expansion by default (-drop-noise defaults true), so it does not become the
// first human turn, and that -drop-noise=false preserves it.
func TestEndToEndDropNoiseDefault(t *testing.T) {
	// A session whose first user turn is a slash-command expansion, followed by a
	// real prompt and a tool call so the sample survives the signal filter.
	noisy := `{"type":"user","sessionId":"s","uuid":"u0","message":{"role":"user","content":"<command-name>/clear</command-name>"}}
{"type":"user","sessionId":"s","uuid":"u1","message":{"role":"user","content":"add a -v flag"}}
{"type":"assistant","sessionId":"s","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}]}}
{"type":"user","sessionId":"s","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package main"}]}}
`
	bin := buildBinary(t)

	// firstPrompt decodes the single openai record and returns meta.first_prompt,
	// which is the unescaped first human turn (the JSON output HTML-escapes "<").
	firstPrompt := func(t *testing.T, out []byte) string {
		t.Helper()
		lines := nonEmptyLines(out)
		if len(lines) != 1 {
			t.Fatalf("got %d records, want 1: %q", len(lines), out)
		}
		var rec struct {
			Meta struct {
				FirstPrompt string `json:"first_prompt"`
			} `json:"meta"`
		}
		if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
			t.Fatalf("record is not JSON: %v\n%s", err, lines[0])
		}
		return rec.Meta.FirstPrompt
	}

	t.Run("default drops noise", func(t *testing.T) {
		cmd := exec.Command(bin, "-format", "openai", "-")
		cmd.Stdin = strings.NewReader(noisy)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if got := firstPrompt(t, out); got != "add a -v flag" {
			t.Errorf("first_prompt = %q, want %q (noise turn should be dropped)", got, "add a -v flag")
		}
	})

	t.Run("escape hatch keeps noise", func(t *testing.T) {
		cmd := exec.Command(bin, "-format", "openai", "-drop-noise=false", "-")
		cmd.Stdin = strings.NewReader(noisy)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if got := firstPrompt(t, out); got != "<command-name>/clear</command-name>" {
			t.Errorf("first_prompt = %q, want the slash-command expansion kept", got)
		}
	})
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
