package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/tmc/cc"
)

func main() {
	var (
		team    = flag.String("team", "", "team name (required)")
		list    = flag.Bool("list", false, "list pending approvals")
		approve = flag.String("approve", "", "approve pending request for agent")
		deny    = flag.String("deny", "", "deny pending request for agent")
		reason  = flag.String("reason", "", "reason for denial")
		auto    = flag.Bool("auto", false, "auto-approve all requests")
		follow  = flag.Bool("follow", false, "watch for approval requests")
		inbox   = flag.String("inbox", "controller", "inbox to monitor for requests")
		format  = flag.String("format", "text", "output format: text, json")
	)
	flag.Parse()

	if *team == "" {
		fmt.Fprintf(os.Stderr, "ccapprove: -team is required\n")
		os.Exit(2)
	}

	var err error
	switch {
	case *list:
		err = doList(*team, *inbox, *format)
	case *approve != "":
		err = doApprove(*team, *inbox, *approve)
	case *deny != "":
		err = doDeny(*team, *inbox, *deny, *reason)
	case *auto:
		err = doAuto(*team, *inbox)
	case *follow:
		err = doFollow(*team, *inbox, *format)
	default:
		err = doList(*team, *inbox, *format)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccapprove: %v\n", err)
		os.Exit(1)
	}
}

type pendingApproval struct {
	Agent     string `json:"agent"`
	Type      string `json:"type"`
	RequestID string `json:"requestId"`
	Timestamp string `json:"timestamp"`
	Detail    string `json:"detail,omitempty"`
}

func findPending(team, inboxName string) ([]pendingApproval, error) {
	msgs, err := cc.ReadInbox(context.Background(), team, inboxName)
	if err != nil {
		return nil, err
	}
	var pending []pendingApproval
	// Track which request IDs have been responded to.
	responded := map[string]bool{}
	for _, m := range msgs {
		sm := cc.ParseMessage(m)
		if sm == nil {
			continue
		}
		if sm.Type == "plan_approval_response" || sm.Type == "permission_response" {
			responded[sm.RequestID] = true
		}
	}
	for _, m := range msgs {
		sm := cc.ParseMessage(m)
		if sm == nil {
			continue
		}
		switch sm.Type {
		case "plan_approval_request":
			if !responded[sm.RequestID] {
				detail := sm.PlanContent
				if len(detail) > 80 {
					detail = detail[:80] + "..."
				}
				pending = append(pending, pendingApproval{
					Agent:     sm.From,
					Type:      "plan",
					RequestID: sm.RequestID,
					Timestamp: sm.Timestamp,
					Detail:    detail,
				})
			}
		case "permission_request":
			if !responded[sm.RequestID] {
				pending = append(pending, pendingApproval{
					Agent:     sm.From,
					Type:      "permission",
					RequestID: sm.RequestID,
					Timestamp: sm.Timestamp,
					Detail:    sm.ToolName + ": " + sm.Description,
				})
			}
		}
	}
	return pending, nil
}

func doList(team, inboxName, format string) error {
	pending, err := findPending(team, inboxName)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		if format == "json" {
			fmt.Println("[]")
		} else {
			fmt.Println("no pending approvals")
		}
		return nil
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(pending)
	}
	for _, p := range pending {
		fmt.Printf("%-20s %-12s %s\n", p.Agent, p.Type, p.RequestID)
		if p.Detail != "" {
			fmt.Printf("  %s\n", p.Detail)
		}
	}
	return nil
}

func doApprove(team, inboxName, agent string) error {
	pending, err := findPending(team, inboxName)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p.Agent != agent {
			continue
		}
		if err := sendResponse(team, inboxName, agent, p, true, ""); err != nil {
			return err
		}
		fmt.Printf("approved %s request from %s (%s)\n", p.Type, p.Agent, p.RequestID)
		return nil
	}
	return fmt.Errorf("no pending approval for agent %q", agent)
}

func doDeny(team, inboxName, agent, reason string) error {
	pending, err := findPending(team, inboxName)
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p.Agent != agent {
			continue
		}
		if err := sendResponse(team, inboxName, agent, p, false, reason); err != nil {
			return err
		}
		fmt.Printf("denied %s request from %s (%s)\n", p.Type, p.Agent, p.RequestID)
		return nil
	}
	return fmt.Errorf("no pending approval for agent %q", agent)
}

func sendResponse(team, inboxName, agent string, p pendingApproval, approved bool, feedback string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var text string
	switch p.Type {
	case "plan":
		resp := map[string]any{
			"type":      "plan_approval_response",
			"requestId": p.RequestID,
			"from":      inboxName,
			"approved":  approved,
			"timestamp": now,
		}
		if feedback != "" {
			resp["feedback"] = feedback
		}
		data, _ := json.Marshal(resp)
		text = string(data)
	case "permission":
		resp := map[string]any{
			"type":      "permission_response",
			"requestId": p.RequestID,
			"from":      inboxName,
			"approved":  approved,
			"timestamp": now,
		}
		data, _ := json.Marshal(resp)
		text = string(data)
	}

	// Write response to both the controller inbox (for tracking) and the agent inbox.
	msg := cc.InboxMessage{
		From:      inboxName,
		Text:      text,
		Timestamp: now,
		Read:      false,
	}
	if err := cc.AppendInbox(context.Background(), team, inboxName, msg); err != nil {
		return fmt.Errorf("record response: %w", err)
	}
	return cc.AppendInbox(context.Background(), team, agent, msg)
}

func doAuto(team, inboxName string) error {
	fmt.Printf("auto-approve mode for team %q (inbox: %s)\n", team, inboxName)
	for {
		pending, err := findPending(team, inboxName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ccapprove: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		for _, p := range pending {
			if err := sendResponse(team, inboxName, p.Agent, p, true, ""); err != nil {
				fmt.Fprintf(os.Stderr, "ccapprove: approve %s: %v\n", p.Agent, err)
				continue
			}
			fmt.Printf("auto-approved %s request from %s (%s)\n", p.Type, p.Agent, p.RequestID)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func doFollow(team, inboxName, format string) error {
	seen := map[string]bool{}
	for {
		pending, err := findPending(team, inboxName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ccapprove: %v\n", err)
			time.Sleep(time.Second)
			continue
		}
		for _, p := range pending {
			if seen[p.RequestID] {
				continue
			}
			seen[p.RequestID] = true
			if format == "json" {
				json.NewEncoder(os.Stdout).Encode(p)
			} else {
				fmt.Printf("[%s] %s %s request from %s\n", p.Timestamp, p.Type, p.RequestID, p.Agent)
				if p.Detail != "" {
					fmt.Printf("  %s\n", p.Detail)
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
}
