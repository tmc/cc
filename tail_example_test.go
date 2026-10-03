package cc_test

import (
	"context"
	"fmt"
	"github.com/tmc/cc"
)

func ExampleNewTail() {
	tail, err := cc.NewTail("testdata/sample-session.jsonl")
	if err != nil {
		fmt.Println(err)
		return
	}
	update, err := tail.Poll(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(update.Replace, update.Summary.SessionID)
	// Output: true sample-session
}

func ExampleTail_Poll() {
	tail, _ := cc.NewTail("testdata/sample-session.jsonl")
	var transcript []cc.Entry
	for i := 0; i < 2; i++ {
		update, err := tail.Poll(context.Background())
		if err != nil {
			fmt.Println(err)
			return
		}
		if update.Replace {
			transcript = nil
		}
		for _, revision := range update.Revisions {
			transcript[revision.Index] = revision.Entry
		}
		transcript = append(transcript, update.Entries...)
	}
	fmt.Println(transcript[0].SessionID)
	// Output: sample-session
}

func ExampleTail_Reset() {
	tail, _ := cc.NewTail("testdata/sample-session.jsonl")
	tail.Poll(context.Background())
	tail.Reset()
	update, err := tail.Poll(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(update.Replace)
	// Output: true
}

func ExampleTail() {
	var tail cc.Tail
	_, err := tail.Poll(context.Background())
	fmt.Println(err)
	// Output: tail is not initialized
}

func ExampleTailRevision() {
	entries := []cc.Entry{{Type: "assistant"}}
	revision := cc.TailRevision{Index: 0, Entry: cc.Entry{Type: "assistant", Origin: "subagent"}}
	entries[revision.Index] = revision.Entry
	fmt.Println(entries[0].Origin)
	// Output: subagent
}

func ExampleTailUpdate() {
	update := cc.TailUpdate{Replace: true, Entries: []cc.Entry{{Type: "user"}}}
	fmt.Println(update.Replace, len(update.Entries))
	// Output: true 1
}
