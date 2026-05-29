package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tmc/cc/cctaskstore"
)

func main() {
	var (
		team    = flag.String("team", "", "team name (required)")
		list    = flag.Bool("list", false, "list all tasks")
		create  = flag.Bool("create", false, "create a new task")
		showID  = flag.String("show", "", "show task by ID")
		update  = flag.String("update", "", "update task by ID")
		delID   = flag.String("delete", "", "delete task by ID")
		waitID  = flag.String("wait", "", "wait for task to complete")
		subject = flag.String("subject", "", "task subject")
		desc    = flag.String("desc", "", "task description")
		status  = flag.String("status", "", "task status: pending, in_progress, completed")
		assign  = flag.String("assign", "", "assign task to agent")
		blocks  = flag.String("blocks", "", "comma-separated task IDs this task blocks")
		timeout = flag.Duration("timeout", 0, "timeout for -wait")
		format  = flag.String("format", "text", "output format: text, json")
	)
	flag.Parse()

	if *team == "" {
		fmt.Fprintf(os.Stderr, "cctask: -team is required\n")
		os.Exit(2)
	}

	store, err := cctaskstore.NewTaskStore(context.Background(), *team)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cctask: %v\n", err)
		os.Exit(1)
	}

	switch {
	case *create:
		err = doCreate(store, *subject, *desc, *assign)
	case *showID != "":
		err = doShow(store, *showID, *format)
	case *update != "":
		err = doUpdate(store, *update, *status, *assign, *blocks, *subject, *desc)
	case *delID != "":
		err = doDelete(store, *delID)
	case *waitID != "":
		err = doWait(store, *waitID, *timeout)
	case *list:
		err = doList(store, *format)
	default:
		err = doList(store, *format)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cctask: %v\n", err)
		os.Exit(1)
	}
}

func doCreate(store *cctaskstore.TaskStore, subject, desc, owner string) error {
	if subject == "" {
		return fmt.Errorf("-subject is required for -create")
	}
	t := cctaskstore.TeamTask{
		Subject:     subject,
		Description: desc,
		Owner:       owner,
	}
	t, err := store.Create(t)
	if err != nil {
		return err
	}
	fmt.Printf("created task %s: %s\n", t.ID, t.Subject)
	return nil
}

func doShow(store *cctaskstore.TaskStore, id, format string) error {
	t, err := store.Get(id)
	if err != nil {
		return err
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(t)
	}
	printTask(t)
	return nil
}

func doUpdate(store *cctaskstore.TaskStore, id, status, owner, blocks, subject, desc string) error {
	t, err := store.Get(id)
	if err != nil {
		return err
	}
	if status != "" {
		t.Status = status
	}
	if owner != "" {
		t.Owner = owner
	}
	if subject != "" {
		t.Subject = subject
	}
	if desc != "" {
		t.Description = desc
	}
	if blocks != "" {
		t.Blocks = strings.Split(blocks, ",")
	}
	if err := store.Update(t); err != nil {
		return err
	}
	fmt.Printf("updated task %s\n", id)
	return nil
}

func doDelete(store *cctaskstore.TaskStore, id string) error {
	if err := store.Delete(id); err != nil {
		return err
	}
	fmt.Printf("deleted task %s\n", id)
	return nil
}

func doWait(store *cctaskstore.TaskStore, id string, timeout time.Duration) error {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		t, err := store.Get(id)
		if err != nil {
			return err
		}
		if t.Status == "completed" {
			fmt.Printf("task %s completed\n", id)
			return nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for task %s (status: %s)", id, t.Status)
		}
		time.Sleep(time.Second)
	}
}

func doList(store *cctaskstore.TaskStore, format string) error {
	tasks, err := store.List()
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		if format == "json" {
			fmt.Println("[]")
		} else {
			fmt.Println("no tasks")
		}
		return nil
	}
	if format == "json" {
		return json.NewEncoder(os.Stdout).Encode(tasks)
	}
	for _, t := range tasks {
		owner := t.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Printf("%-4s %-12s %-16s %s\n", t.ID, t.Status, owner, t.Subject)
	}
	return nil
}

func printTask(t cctaskstore.TeamTask) {
	fmt.Printf("ID:          %s\n", t.ID)
	fmt.Printf("Subject:     %s\n", t.Subject)
	fmt.Printf("Status:      %s\n", t.Status)
	if t.Owner != "" {
		fmt.Printf("Owner:       %s\n", t.Owner)
	}
	if t.Description != "" {
		fmt.Printf("Description: %s\n", t.Description)
	}
	if t.ActiveForm != "" {
		fmt.Printf("ActiveForm:  %s\n", t.ActiveForm)
	}
	if len(t.Blocks) > 0 {
		fmt.Printf("Blocks:      %s\n", strings.Join(t.Blocks, ", "))
	}
	if len(t.BlockedBy) > 0 {
		fmt.Printf("BlockedBy:   %s\n", strings.Join(t.BlockedBy, ", "))
	}
}
