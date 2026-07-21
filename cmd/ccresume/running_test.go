package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tmc/cc"
)

func TestResumeToken(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"claude", []string{"-r", "f339494e-a034-4c0f-943a-a9fedd533b29"}, "f339494e-a034-4c0f-943a-a9fedd533b29"},
		{"codex", []string{"resume", "019f7246-1376-7cd2-bfb3-e86d231ba7da"}, "019f7246-1376-7cd2-bfb3-e86d231ba7da"},
		{"opencode", []string{"--session", "ses_abc"}, "ses_abc"},
		{"agy", []string{"--conversation", "conv-uuid"}, "conv-uuid"},
		{"pi path", []string{"--session", "/home/u/.pi/sessions/x.jsonl"}, "/home/u/.pi/sessions/x.jsonl"},
		{"fresh session, no args", nil, ""},
		{"empty args", []string{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeToken(tt.args); got != tt.want {
				t.Errorf("resumeToken(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestCommandInvokes(t *testing.T) {
	tests := []struct {
		cmdline string
		bin     string
		want    bool
	}{
		{"claude --dangerously-skip-permissions -r abc", "claude", true},
		{"/opt/homebrew/bin/claude -r abc", "claude", true},           // absolute path, base matches
		{"/Users/example/.local/bin/codex resume abc", "codex", true}, // absolute path, base matches
		{"codex resume abc", "claude", false},                         // wrong binary
		{"node /path/to/claude.js -r abc", "claude", false},           // claude only as an argument, not argv[0]
		{"claude", "claude", true},                                    // bare binary, no args
	}
	for _, tt := range tests {
		if got := commandInvokes(tt.cmdline, tt.bin); got != tt.want {
			t.Errorf("commandInvokes(%q, %q) = %v, want %v", tt.cmdline, tt.bin, got, tt.want)
		}
	}
}

func TestLastPathElem(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"claude", "claude"},
		{"/opt/homebrew/bin/claude", "claude"},
		{"/usr/bin/codex", "codex"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := lastPathElem(tt.in); got != tt.want {
			t.Errorf("lastPathElem(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunningPIDs(t *testing.T) {
	procs := []runningProc{
		{pid: 100, cmdline: "claude --dangerously-skip-permissions -r sess-A"},
		{pid: 200, cmdline: "codex --dangerously-bypass-approvals-and-sandbox resume sess-B"},
		{pid: 300, cmdline: "claude --dangerously-skip-permissions"}, // fresh, no id
		{pid: 400, cmdline: "claude -r sess-A"},                      // second process on sess-A
		{pid: 500, cmdline: "grep sess-A"},                           // an unrelated process that mentions the id
	}
	tests := []struct {
		name  string
		bin   string
		token string
		want  []int
	}{
		{"single match", "claude", "sess-A", []int{100, 400}},
		{"codex match", "codex", "sess-B", []int{200}},
		{"wrong binary", "codex", "sess-A", nil},   // sess-A is claude's, not codex's
		{"empty token (fresh)", "claude", "", nil}, // no id to match
		{"token not running", "claude", "sess-Z", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runningPIDs(procs, tt.bin, tt.token)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("runningPIDs(%q, %q) = %v, want %v", tt.bin, tt.token, got, tt.want)
			}
		})
	}
}

func TestRunningMarker(t *testing.T) {
	procs := []runningProc{
		{pid: 100, cmdline: "claude -r sess-A"},
		{pid: 400, cmdline: "claude -r sess-A"}, // duplicate: same session, two panes
		{pid: 200, cmdline: "codex resume sess-B"},
	}
	tests := []struct {
		name string
		bin  string
		args []string
		want string
	}{
		{"not running", "claude", []string{"-r", "sess-Z"}, ""},
		{"single process", "codex", []string{"resume", "sess-B"}, "RUNNING"},
		{"multiple processes", "claude", []string{"-r", "sess-A"}, "RUNNING ×2"},
		{"fresh session, no id", "claude", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runningMarker(procs, tt.bin, tt.args); got != tt.want {
				t.Errorf("runningMarker(%q, %v) = %q, want %q", tt.bin, tt.args, got, tt.want)
			}
		})
	}
}

func TestAnnotateLineRunningAtEnd(t *testing.T) {
	procs := []runningProc{{pid: 100, cmdline: "claude -r sess-A"}}
	r := resolvedMatch{entry: cc.IndexEntry{MessageCount: 12}}
	args := []string{"-r", "sess-A"}
	const base = "claude -r sess-A"

	// -show-is-running only: a lone "# RUNNING pid 100" trailing comment.
	*runningFlag = true
	*infoFlag = false
	defer func() { *runningFlag = false; *infoFlag = false }()
	got := annotateLine(base, "q", r, false, procs, "claude", args, false)
	if !strings.HasPrefix(got, base) {
		t.Errorf("annotateLine dropped the command: %q", got)
	}
	if !strings.HasSuffix(got, "# RUNNING") {
		t.Errorf("running-only annotateLine = %q, want it to end with the RUNNING comment", got)
	}

	// both flags: info comment first, RUNNING last (at the very end of the line).
	*infoFlag = true
	got = annotateLine(base, "q", r, true, procs, "claude", args, false)
	if !strings.HasSuffix(got, "# RUNNING") {
		t.Errorf("combined annotateLine = %q, want RUNNING at the end", got)
	}
	iInfo := strings.Index(got, "12×")
	iRun := strings.Index(got, "RUNNING")
	if iInfo < 0 || iRun < 0 || iInfo > iRun {
		t.Errorf("combined annotateLine = %q, want the info detail before RUNNING", got)
	}

	// not running: no RUNNING comment, info still present.
	got = annotateLine(base, "q", r, true, nil, "claude", args, false)
	if strings.Contains(got, "RUNNING") {
		t.Errorf("not-running annotateLine = %q, should have no RUNNING comment", got)
	}
	if !strings.Contains(got, "12×") {
		t.Errorf("not-running annotateLine = %q, want the info detail", got)
	}
}

func TestScanProcessesParsesSelf(t *testing.T) {
	// A light smoke test that scanProcesses returns a plausible snapshot on this
	// platform: the running test binary itself must appear with a positive PID.
	procs := scanProcesses()
	if len(procs) == 0 {
		t.Skip("ps returned no processes (unsupported platform)")
	}
	for _, p := range procs {
		if p.pid <= 0 {
			t.Fatalf("scanProcesses returned non-positive pid %d for %q", p.pid, p.cmdline)
		}
	}
}
