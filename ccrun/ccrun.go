package ccrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

// Options configures a single Claude Code run.
type Options struct {
	// Binary is the CLI to invoke. Empty means "claude" on PATH.
	Binary string
	// ExtraArgs are passed to the binary after the required print-mode flags.
	ExtraArgs []string
	// WorkDir is the command working directory. Empty uses the current process
	// directory.
	WorkDir string
	// SystemPreamble is fixed system framing, prepended to SkillText.
	SystemPreamble string
	// SkillText is rendered skill markdown, the trainable part of the system
	// prompt.
	SkillText string
}

// Result is the parsed outcome of a single Claude Code run.
type Result struct {
	SessionID  string
	Output     string
	Transcript []cc.Entry
	RawStdout  []byte
	RawStderr  []byte
}

// Run executes Claude Code once with prompt and returns its parsed result.
func Run(ctx context.Context, prompt string, opts Options) (*Result, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "claude"
	}
	args := []string{"-p", prompt, "--output-format", "json"}
	if system := joinSystem(opts.SystemPreamble, opts.SkillText); system != "" {
		args = append(args, "--append-system-prompt", system)
	}
	args = append(args, opts.ExtraArgs...)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = opts.WorkDir
	setupCommand(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	cleanupCommand(cmd)

	rawStdout := append([]byte(nil), stdout.Bytes()...)
	rawStderr := append([]byte(nil), stderr.Bytes()...)
	if err != nil {
		res, perr := parseClaudeResult(rawStdout)
		if perr == nil && res.Result != "" {
			out := resultFromEnvelope(ctx, res, rawStdout, rawStderr, opts.WorkDir)
			return out, claudeFailure(res, string(rawStderr))
		}
		return &Result{RawStdout: rawStdout, RawStderr: rawStderr}, execFailure(err, string(rawStderr))
	}

	res, err := parseClaudeResult(rawStdout)
	if err != nil {
		return &Result{RawStdout: rawStdout, RawStderr: rawStderr}, err
	}
	out := resultFromEnvelope(ctx, res, rawStdout, rawStderr, opts.WorkDir)
	if res.IsError {
		return out, claudeFailure(res, string(rawStderr))
	}
	return out, nil
}

func resultFromEnvelope(ctx context.Context, res claudeResult, stdout, stderr []byte, workDir string) *Result {
	out := &Result{
		SessionID: res.SessionID,
		Output:    strings.TrimSpace(res.Result),
		RawStdout: append([]byte(nil), stdout...),
		RawStderr: append([]byte(nil), stderr...),
	}
	if entries, err := readTranscript(ctx, res.SessionID, workDir); err == nil {
		out.Transcript = entries
	}
	return out
}

func joinSystem(preamble, skill string) string {
	var parts []string
	if preamble != "" {
		parts = append(parts, preamble)
	}
	if skill = strings.TrimRight(skill, "\n"); skill != "" {
		parts = append(parts, skill)
	}
	return strings.Join(parts, "\n\n")
}

type claudeResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	NumTurns  int    `json:"num_turns"`
}

func parseClaudeResult(stdout []byte) (claudeResult, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return claudeResult{}, fmt.Errorf("ccrun: empty output")
	}
	switch trimmed[0] {
	case '{':
		var res claudeResult
		if err := json.Unmarshal(trimmed, &res); err != nil {
			return claudeResult{}, fmt.Errorf("ccrun: parse result: %w", err)
		}
		return res, nil
	case '[':
		var events []claudeResult
		if err := json.Unmarshal(trimmed, &events); err != nil {
			return claudeResult{}, fmt.Errorf("ccrun: parse events: %w", err)
		}
		if len(events) == 0 {
			return claudeResult{}, fmt.Errorf("ccrun: empty event array")
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == "result" {
				return events[i], nil
			}
		}
		return events[len(events)-1], nil
	default:
		return claudeResult{}, fmt.Errorf("ccrun: unrecognized output (first byte %q)", trimmed[0])
	}
}

func readTranscript(ctx context.Context, sessionID, workDir string) ([]cc.Entry, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("ccrun: empty session id")
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("get working directory: %w", err)
		}
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	home, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, fmt.Errorf("find claude home: %w", err)
	}
	path := filepath.Join(home, "projects", ccpaths.EncodePath(abs), sessionID+".jsonl")
	entries, err := cc.ReadFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	return entries, nil
}

func claudeFailure(res claudeResult, stderr string) error {
	if res.Result != "" {
		return fmt.Errorf("%s", res.Result)
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%s", s)
	}
	return fmt.Errorf("claude reported an error")
}

func execFailure(err error, stderr string) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%v: %s", err, s)
	}
	return err
}
