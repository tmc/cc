package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tmc/cc"
)

func main() {
	var (
		team      = flag.String("team", "", "team name (required)")
		to        = flag.String("to", "", "send message to agent")
		from      = flag.String("from", "", "read messages from agent's inbox")
		unread    = flag.Bool("unread", false, "read only unread messages")
		follow    = flag.Bool("follow", false, "follow inbox for new messages")
		broadcast = flag.String("broadcast", "", "broadcast message to all agents")
		sender    = flag.String("sender", "controller", "sender name for messages")
		msgType   = flag.String("type", "", "structured message type")
		taskID    = flag.String("task-id", "", "task ID for task_assignment messages")
		subject   = flag.String("subject", "", "subject for task_assignment messages")
		toolName  = flag.String("tool-name", "", "tool name for permission_request messages")
		format    = flag.String("format", "text", "output format: text, json")
	)
	flag.Parse()

	if *team == "" {
		fmt.Fprintf(os.Stderr, "ccinbox: -team is required\n")
		os.Exit(2)
	}

	var err error
	switch {
	case *broadcast != "":
		err = doBroadcast(*team, *sender, *broadcast)
	case *to != "":
		err = doSend(*team, *to, *sender, *msgType, *taskID, *subject, *toolName)
	case *from != "":
		if *follow {
			err = doFollow(*team, *from, *format)
		} else if *unread {
			err = doReadUnread(*team, *from, *format)
		} else {
			err = doRead(*team, *from, *format)
		}
	default:
		fmt.Fprintf(os.Stderr, "ccinbox: -to, -from, or -broadcast is required\n")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccinbox: %v\n", err)
		os.Exit(1)
	}
}

func doSend(team, to, sender, msgType, taskID, subject, toolName string) error {
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	text := string(body)

	if msgType != "" {
		text, err = buildStructuredMessage(msgType, text, sender, taskID, subject, toolName)
		if err != nil {
			return err
		}
	}

	msg := cc.InboxMessage{
		From: sender,
		Text: text,
		Read: false,
	}
	return cc.AppendInbox(context.Background(), team, to, msg)
}

func buildStructuredMessage(msgType, body, sender, taskID, subject, toolName string) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	var m any
	switch msgType {
	case "plain_text":
		m = map[string]string{"type": "plain_text", "text": body}
	case "task_assignment":
		m = map[string]string{
			"type":        "task_assignment",
			"taskId":      taskID,
			"subject":     subject,
			"description": body,
			"assignedBy":  sender,
			"timestamp":   now,
		}
	case "shutdown_request":
		m = map[string]string{
			"type":      "shutdown_request",
			"requestId": fmt.Sprintf("shutdown-%d@%s", time.Now().UnixMilli(), sender),
			"from":      sender,
			"reason":    body,
			"timestamp": now,
		}
	case "idle_notification":
		m = map[string]string{
			"type":       "idle_notification",
			"from":       sender,
			"timestamp":  now,
			"idleReason": body,
		}
	case "plan_approval_request":
		m = map[string]string{
			"type":        "plan_approval_request",
			"requestId":   fmt.Sprintf("plan-%d@%s", time.Now().UnixMilli(), sender),
			"from":        sender,
			"planContent": body,
			"timestamp":   now,
		}
	case "permission_request":
		m = map[string]string{
			"type":        "permission_request",
			"requestId":   fmt.Sprintf("perm-%d@%s", time.Now().UnixMilli(), sender),
			"from":        sender,
			"toolName":    toolName,
			"description": body,
			"timestamp":   now,
		}
	default:
		return "", fmt.Errorf("unknown message type %q", msgType)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}
	return string(data), nil
}

func doBroadcast(team, sender, text string) error {
	cfg, err := cc.ReadTeamConfig(team)
	if err != nil {
		return err
	}
	msg := cc.InboxMessage{
		From: sender,
		Text: text,
		Read: false,
	}
	for _, m := range cfg.Members {
		if err := cc.AppendInbox(context.Background(), team, m.Name, msg); err != nil {
			fmt.Fprintf(os.Stderr, "ccinbox: error sending to %s: %v\n", m.Name, err)
		}
	}
	return nil
}

func doRead(team, agent, format string) error {
	msgs, err := cc.ReadInbox(context.Background(), team, agent)
	if err != nil {
		return err
	}
	return printMessages(msgs, format)
}

func doReadUnread(team, agent, format string) error {
	msgs, err := cc.ReadUnread(context.Background(), team, agent)
	if err != nil {
		return err
	}
	return printMessages(msgs, format)
}

func doFollow(team, agent, format string) error {
	seen := 0
	for {
		msgs, err := cc.ReadInbox(context.Background(), team, agent)
		if err != nil {
			return err
		}
		if len(msgs) > seen {
			if err := printMessages(msgs[seen:], format); err != nil {
				return err
			}
			seen = len(msgs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func printMessages(msgs []cc.InboxMessage, format string) error {
	if len(msgs) == 0 {
		return nil
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(msgs)
	}
	for _, m := range msgs {
		readMark := " "
		if m.Read {
			readMark = "*"
		}
		fmt.Printf("[%s] %s from=%s\n", readMark, m.Timestamp, m.From)
		if sm := cc.ParseMessage(m); sm != nil {
			fmt.Printf("  type=%s\n", sm.Type)
		}
		fmt.Printf("  %s\n", m.Text)
	}
	return nil
}
