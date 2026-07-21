package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// runningProc is one entry from the process table: its PID and full command
// line as reported by ps.
type runningProc struct {
	pid     int
	cmdline string
}

// scanProcesses snapshots the process table once via ps, returning every
// process with its PID and full command line. It is called at most once per
// run (when -running is set) so the whole output can be annotated from a single
// consistent snapshot. A ps failure yields an empty list, so annotation simply
// reports nothing rather than erroring the whole command.
func scanProcesses() []runningProc {
	// -A: all processes; -ww: do not truncate the command column; the two
	// trailing '=' suppress the header so every line is "<pid> <command>".
	out, err := exec.Command("ps", "-Aww", "-o", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	var procs []runningProc
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pidStr, cmd, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			continue
		}
		procs = append(procs, runningProc{pid: pid, cmdline: strings.TrimSpace(cmd)})
	}
	return procs
}

// resumeToken returns the identifier a resume invocation is keyed on: the last
// argument (a session id for claude/codex/gemini/opencode/agy, or the session
// file path for pi). This is "the last part of the command" a running process
// would carry, so a match on it means that session is already open. Invocations
// with no arguments (a fresh session, no id) have no token and return "".
func resumeToken(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[len(args)-1]
}

// runningPIDs returns the PIDs of processes that are resuming the given session,
// matched by the resume token (session id or file path) appearing in a process
// whose command also invokes bin. Requiring bin guards against a stray token
// substring matching an unrelated process; the token itself (a UUID or an
// absolute path) is already near-unique. The result is nil when nothing matches.
func runningPIDs(procs []runningProc, bin, token string) []int {
	if token == "" {
		return nil
	}
	var pids []int
	for _, p := range procs {
		if !commandInvokes(p.cmdline, bin) {
			continue
		}
		if strings.Contains(p.cmdline, token) {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

// commandInvokes reports whether a process command line runs the named binary,
// accepting either a bare name (claude) or an absolute path whose base is the
// name (/opt/homebrew/bin/claude). It inspects only the first field so a binary
// name appearing later as an argument does not count.
func commandInvokes(cmdline, bin string) bool {
	first, _, _ := strings.Cut(cmdline, " ")
	if first == bin {
		return true
	}
	return lastPathElem(first) == bin
}

// lastPathElem returns the final '/'-separated element of s, or s itself when it
// has no separator. It intentionally avoids path/filepath so the comparison is
// against the literal ps output rather than a cleaned path.
func lastPathElem(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// runningMarker returns the -show-is-running annotation for a resume invocation,
// or "" when the session is not currently running. It is appended as its own
// trailing comment at the end of the line, so it reads as plain shell after the
// '#': "RUNNING" for a single process, "RUNNING ×N" when several processes share
// the session.
func runningMarker(procs []runningProc, bin string, args []string) string {
	pids := runningPIDs(procs, bin, resumeToken(args))
	switch {
	case len(pids) == 0:
		return ""
	case len(pids) == 1:
		return "RUNNING"
	default:
		return "RUNNING ×" + strconv.Itoa(len(pids))
	}
}
