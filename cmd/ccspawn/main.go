package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tmc/cc/ccteamcfg"
)

func main() {
	var (
		team         = flag.String("team", "", "team name (required)")
		agent        = flag.String("agent", "", "agent name")
		model        = flag.String("model", "", "model to use")
		cwd          = flag.String("cwd", "", "working directory")
		agentType    = flag.String("type", "general-purpose", "agent type")
		permMode     = flag.String("permission-mode", "", "permission mode")
		allowedTools = flag.String("allowed-tools", "", "comma-separated allowed tools")
		kill         = flag.Bool("kill", false, "kill agent process")
		list         = flag.Bool("list", false, "list agent processes")
		claudeBinary = flag.String("claude-binary", "claude", "claude binary name")
	)
	flag.Parse()

	if *team == "" {
		fmt.Fprintf(os.Stderr, "ccspawn: -team is required\n")
		os.Exit(2)
	}

	var err error
	switch {
	case *list:
		err = doList(*team)
	case *kill:
		if *agent == "" {
			fmt.Fprintf(os.Stderr, "ccspawn: -agent is required with -kill\n")
			os.Exit(2)
		}
		err = doKill(*team, *agent)
	case *agent != "":
		err = doSpawn(*team, *agent, *model, *cwd, *agentType, *permMode, *allowedTools, *claudeBinary)
	default:
		fmt.Fprintf(os.Stderr, "ccspawn: -agent or -list is required\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccspawn: %v\n", err)
		os.Exit(1)
	}
}

func doSpawn(team, agent, model, cwd, agentType, permMode, allowedTools, claudeBinary string) error {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
	}

	// Register agent in team config.
	member := ccteamcfg.TeamMember{
		AgentID:   agent + "@" + team,
		Name:      agent,
		AgentType: agentType,
		Model:     model,
		JoinedAt:  time.Now().UnixMilli(),
		CWD:       cwd,
	}
	if err := ccteamcfg.AddTeamMember(team, member); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}

	// Build claude command args.
	args := []string{
		"--teammate-mode", "auto",
		"--agent-id", agent + "@" + team,
		"--agent-name", agent,
		"--team-name", team,
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if agentType != "" {
		args = append(args, "--agent-type", agentType)
	}
	if permMode != "" {
		args = append(args, "--permission-mode", permMode)
	}
	if allowedTools != "" {
		for tool := range strings.SplitSeq(allowedTools, ",") {
			tool = strings.TrimSpace(tool)
			if tool != "" {
				args = append(args, "--allowedTools", tool)
			}
		}
	}

	// Resolve claude binary path.
	binPath, err := exec.LookPath(claudeBinary)
	if err != nil {
		return fmt.Errorf("find claude binary: %w", err)
	}

	// Spawn process.
	cmd := exec.Command(binPath, args...)
	cmd.Dir = cwd
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1")

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	// Write PID file.
	if err := writePID(team, agent, cmd.Process.Pid); err != nil {
		fmt.Fprintf(os.Stderr, "ccspawn: warning: %v\n", err)
	}

	// Forward signals.
	sigch := make(chan os.Signal, 1)
	signal.Notify(sigch, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigch
		cmd.Process.Signal(sig)
	}()

	// Wait for exit.
	err = cmd.Wait()
	removePID(team, agent)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("agent exited: %w", err)
	}
	return nil
}

func doKill(team, agent string) error {
	pid, err := readPID(team, agent)
	if err != nil {
		return fmt.Errorf("read pid: %w", err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	// Send SIGTERM first.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sigterm: %w", err)
	}
	fmt.Printf("sent SIGTERM to %s (pid %d)\n", agent, pid)

	// Wait up to 5s then SIGKILL.
	done := make(chan error, 1)
	go func() {
		_, err := proc.Wait()
		done <- err
	}()
	select {
	case <-done:
		fmt.Printf("agent %s exited\n", agent)
	case <-time.After(5 * time.Second):
		proc.Signal(syscall.SIGKILL)
		fmt.Printf("sent SIGKILL to %s (pid %d)\n", agent, pid)
	}

	removePID(team, agent)
	return nil
}

func doList(team string) error {
	dir, err := pidsDir(team)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("no agents")
			return nil
		}
		return fmt.Errorf("list pids: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".pid" {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".pid")
		pid, err := readPID(team, name)
		if err != nil {
			continue
		}
		alive := isAlive(pid)
		status := "dead"
		if alive {
			status = "running"
		}
		fmt.Printf("%-20s pid=%-8d %s\n", name, pid, status)
	}
	return nil
}

func pidsDir(team string) (string, error) {
	dir, err := ccteamcfg.TeamDir(team)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pids"), nil
}

func writePID(team, agent string, pid int) error {
	dir, err := pidsDir(team)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create pids dir: %w", err)
	}
	path := filepath.Join(dir, agent+".pid")
	return os.WriteFile(path, []byte(strconv.Itoa(pid)), 0o644)
}

func readPID(team, agent string) (int, error) {
	dir, err := pidsDir(team)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(filepath.Join(dir, agent+".pid"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func removePID(team, agent string) {
	dir, err := pidsDir(team)
	if err != nil {
		return
	}
	os.Remove(filepath.Join(dir, agent+".pid"))
}

func isAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
