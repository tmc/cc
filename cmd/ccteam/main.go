package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tmc/cc"
)

func main() {
	var (
		create = flag.String("create", "", "create a team with this name")
		show   = flag.String("show", "", "show team config")
		del    = flag.String("delete", "", "delete team and data")
		add    = flag.String("add", "", "add member to team")
		remove = flag.String("remove", "", "remove member from team")
		agent  = flag.String("agent", "", "agent name (for -add/-remove)")
		model  = flag.String("model", "", "model for agent")
		cwd    = flag.String("cwd", "", "working directory for agent")
		format = flag.String("format", "text", "output format: text, json")
	)
	flag.Parse()

	var err error
	switch {
	case *create != "":
		err = doCreate(*create)
	case *show != "":
		err = doShow(*show, *format)
	case *del != "":
		err = doDelete(*del)
	case *add != "":
		err = doAdd(*add, *agent, *model, *cwd)
	case *remove != "":
		err = doRemove(*remove, *agent)
	default:
		err = doList(*format)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccteam: %v\n", err)
		os.Exit(1)
	}
}

func doCreate(name string) error {
	cfg := &cc.TeamConfig{
		Name:          name,
		CreatedAt:     time.Now().UnixMilli(),
		LeadAgentID:   "controller@" + name,
		LeadSessionID: "",
		Members:       []cc.TeamMember{},
	}
	if err := cc.WriteTeamConfig(name, cfg); err != nil {
		return err
	}
	// Create inboxes directory.
	dir, err := cc.InboxDir(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create inboxes dir: %w", err)
	}
	fmt.Printf("created team %q\n", name)
	return nil
}

func doShow(name, format string) error {
	cfg, err := cc.ReadTeamConfig(name)
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(cfg)
	}
	fmt.Printf("Team: %s\n", cfg.Name)
	if cfg.Description != "" {
		fmt.Printf("Description: %s\n", cfg.Description)
	}
	fmt.Printf("Created: %s\n", time.UnixMilli(cfg.CreatedAt).Format(time.RFC3339))
	fmt.Printf("Lead: %s\n", cfg.LeadAgentID)
	fmt.Printf("Members: %d\n", len(cfg.Members))
	for _, m := range cfg.Members {
		model := m.Model
		if model == "" {
			model = "default"
		}
		fmt.Printf("  %-20s %-20s %s\n", m.Name, m.AgentType, model)
	}
	return nil
}

func doDelete(name string) error {
	if err := cc.DeleteTeam(name); err != nil {
		return err
	}
	fmt.Printf("deleted team %q\n", name)
	return nil
}

func doAdd(teamName, agentName, model, cwd string) error {
	if agentName == "" {
		return fmt.Errorf("-agent is required with -add")
	}
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get cwd: %w", err)
		}
	}
	member := cc.TeamMember{
		AgentID:   agentName + "@" + teamName,
		Name:      agentName,
		AgentType: "general-purpose",
		Model:     model,
		JoinedAt:  time.Now().UnixMilli(),
		CWD:       cwd,
	}
	if err := cc.AddTeamMember(teamName, member); err != nil {
		return err
	}
	fmt.Printf("added agent %q to team %q\n", agentName, teamName)
	return nil
}

func doRemove(teamName, agentName string) error {
	if agentName == "" {
		return fmt.Errorf("-agent is required with -remove")
	}
	agentID := agentName + "@" + teamName
	if err := cc.RemoveTeamMember(teamName, agentID); err != nil {
		return err
	}
	fmt.Printf("removed agent %q from team %q\n", agentName, teamName)
	return nil
}

func doList(format string) error {
	names, err := cc.ListTeams()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		if format != "json" {
			fmt.Println("no teams")
		} else {
			fmt.Println("[]")
		}
		return nil
	}
	if format == "json" {
		var configs []*cc.TeamConfig
		for _, name := range names {
			cfg, err := cc.ReadTeamConfig(name)
			if err != nil {
				continue
			}
			configs = append(configs, cfg)
		}
		return json.NewEncoder(os.Stdout).Encode(configs)
	}
	for _, name := range names {
		cfg, err := cc.ReadTeamConfig(name)
		if err != nil {
			fmt.Printf("%-20s (error reading config)\n", name)
			continue
		}
		fmt.Printf("%-20s %d members\n", name, len(cfg.Members))
	}
	return nil
}
