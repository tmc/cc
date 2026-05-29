package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tmc/cc/ccinboxstore"
	"github.com/tmc/cc/ccteamcfg"
)

func main() {
	var (
		team     = flag.String("team", "", "team name (required)")
		list     = flag.Bool("list", false, "list agents with status")
		status   = flag.String("status", "", "show status for agent")
		idle     = flag.Bool("idle", false, "list idle agents")
		waitIdle = flag.String("wait-idle", "", "wait for agent to become idle")
		timeout  = flag.Duration("timeout", 0, "timeout for -wait-idle")
		format   = flag.String("format", "text", "output format: text, json")
	)
	flag.Parse()

	if *team == "" {
		fmt.Fprintf(os.Stderr, "ccagent: -team is required\n")
		os.Exit(2)
	}

	var err error
	switch {
	case *list:
		err = doList(*team, *format)
	case *status != "":
		err = doStatus(*team, *status, *format)
	case *idle:
		err = doIdle(*team, *format)
	case *waitIdle != "":
		err = doWaitIdle(*team, *waitIdle, *timeout)
	default:
		err = doList(*team, *format)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccagent: %v\n", err)
		os.Exit(1)
	}
}

type agentInfo struct {
	Name    string `json:"name"`
	Model   string `json:"model,omitempty"`
	Type    string `json:"type"`
	PID     int    `json:"pid,omitempty"`
	Alive   bool   `json:"alive"`
	Idle    bool   `json:"idle"`
	IdleSrc string `json:"idleReason,omitempty"`
}

func doList(team, format string) error {
	cfg, err := ccteamcfg.ReadTeamConfig(team)
	if err != nil {
		return err
	}
	var agents []agentInfo
	for _, m := range cfg.Members {
		info := agentInfo{
			Name:  m.Name,
			Model: m.Model,
			Type:  m.AgentType,
		}
		pid, alive := checkPID(team, m.Name)
		info.PID = pid
		info.Alive = alive
		info.Idle, info.IdleSrc = checkIdle(team, m.Name)
		agents = append(agents, info)
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(agents)
	}
	if len(agents) == 0 {
		fmt.Println("no agents")
		return nil
	}
	for _, a := range agents {
		status := "dead"
		if a.Alive {
			status = "running"
		}
		if a.Idle {
			status = "idle"
		}
		model := a.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("%-20s %-10s %-16s pid=%-8d %s\n", a.Name, status, model, a.PID, a.Type)
	}
	return nil
}

func doStatus(team, agent, format string) error {
	cfg, err := ccteamcfg.ReadTeamConfig(team)
	if err != nil {
		return err
	}
	var member *ccteamcfg.TeamMember
	for i, m := range cfg.Members {
		if m.Name == agent {
			member = &cfg.Members[i]
			break
		}
	}
	if member == nil {
		return fmt.Errorf("agent %q not found in team %q", agent, team)
	}
	info := agentInfo{
		Name:  member.Name,
		Model: member.Model,
		Type:  member.AgentType,
	}
	info.PID, info.Alive = checkPID(team, agent)
	info.Idle, info.IdleSrc = checkIdle(team, agent)
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(info)
	}
	status := "dead"
	if info.Alive {
		status = "running"
	}
	if info.Idle {
		status = "idle"
	}
	fmt.Printf("Agent:  %s\n", info.Name)
	fmt.Printf("Status: %s\n", status)
	fmt.Printf("Type:   %s\n", info.Type)
	if info.Model != "" {
		fmt.Printf("Model:  %s\n", info.Model)
	}
	if info.PID != 0 {
		fmt.Printf("PID:    %d\n", info.PID)
	}
	if info.IdleSrc != "" {
		fmt.Printf("Idle:   %s\n", info.IdleSrc)
	}
	return nil
}

func doIdle(team, format string) error {
	cfg, err := ccteamcfg.ReadTeamConfig(team)
	if err != nil {
		return err
	}
	var idle []agentInfo
	for _, m := range cfg.Members {
		isIdle, reason := checkIdle(team, m.Name)
		if isIdle {
			idle = append(idle, agentInfo{
				Name:    m.Name,
				Type:    m.AgentType,
				Idle:    true,
				IdleSrc: reason,
			})
		}
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(idle)
	}
	if len(idle) == 0 {
		fmt.Println("no idle agents")
		return nil
	}
	for _, a := range idle {
		fmt.Printf("%-20s %s\n", a.Name, a.IdleSrc)
	}
	return nil
}

func doWaitIdle(team, agent string, timeout time.Duration) error {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		isIdle, _ := checkIdle(team, agent)
		if isIdle {
			fmt.Printf("agent %s is idle\n", agent)
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %s to become idle", agent)
		}
		time.Sleep(time.Second)
	}
}

func checkPID(team, agent string) (int, bool) {
	dir, err := ccteamcfg.TeamDir(team)
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(dir, "pids", agent+".pid"))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	err = proc.Signal(syscall.Signal(0))
	return pid, err == nil
}

func checkIdle(team, agent string) (bool, string) {
	msgs, err := ccinboxstore.ReadInbox(context.Background(), team, agent)
	if err != nil {
		return false, ""
	}
	// Look for most recent idle notification (scan backwards).
	for i := len(msgs) - 1; i >= 0; i-- {
		sm := ccinboxstore.ParseMessage(msgs[i])
		if sm == nil {
			continue
		}
		if sm.Type == "idle_notification" {
			return true, sm.IdleReason
		}
		// Any non-idle structured message after the last idle means not idle.
		if sm.Type == "task_assignment" || sm.Type == "plain_text" {
			return false, ""
		}
	}
	return false, ""
}
