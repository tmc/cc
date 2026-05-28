package cc_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/tmc/cc"
)

func ExampleNewReader() {
	const jsonl = `{"type":"user","sessionId":"s1","uuid":"u1","message":{"role":"user","content":"hello"}}
{"type":"assistant","sessionId":"s1","uuid":"u2","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}
`
	r := cc.NewReader(context.Background(), strings.NewReader(jsonl))
	for r.Next() {
		e := r.Entry()
		fmt.Println(e.Type, e.UUID)
	}
	if err := r.Err(); err != nil {
		fmt.Println("err:", err)
	}
	// Output:
	// user u1
	// assistant u2
}

func ExampleExtractText() {
	const content = `[{"type":"text","text":"hello   world"}]`
	fmt.Println(cc.ExtractText([]byte(content)))
	// Output: hello world
}

func ExampleSummarize() {
	const jsonl = `{"type":"user","sessionId":"s1","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"do the thing"}]}}
{"type":"assistant","sessionId":"s1","uuid":"u2","message":{"role":"assistant","model":"claude","content":[{"type":"text","text":"ok"}]}}
`
	entries, err := cc.ReadAll(context.Background(), strings.NewReader(jsonl))
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	s := cc.Summarize("example.jsonl", entries)
	fmt.Println(s.SessionID, s.UserMessages, s.AsstMessages, s.FirstPrompt)
	// Output: s1 1 1 do the thing
}
