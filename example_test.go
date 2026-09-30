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

func ExampleClassify() {
	fmt.Println(cc.Classify(cc.Entry{Origin: "human"}))
	fmt.Println(cc.Classify(cc.Entry{Origin: "auto-continuation"}))
	// Output:
	// human
	// system
}

func ExampleClassificationRules() {
	rules := append([]cc.ClassificationRule{func(e cc.Entry) string {
		if e.Content == "consumer event" {
			return "peer"
		}
		return ""
	}}, cc.ClassificationRules()...)
	for _, rule := range rules {
		if kind := rule(cc.Entry{Content: "consumer event"}); kind != "" {
			fmt.Println(kind)
			break
		}
	}
	// Output: peer
}

func ExampleClassificationRule() {
	var rule cc.ClassificationRule = func(e cc.Entry) string {
		if e.Content == "consumer event" {
			return "peer"
		}
		return ""
	}
	fmt.Println(rule(cc.Entry{Content: "consumer event"}))
	// Output: peer
}

func ExampleNewestRateLimits() {
	sample := cc.NewestRateLimits([]cc.Entry{{RateLimits: &cc.RateLimits{
		Primary: &cc.RateLimit{UsedPercent: 25, WindowMinutes: 300},
	}}})
	fmt.Println(sample.Primary.UsedPercent)
	// Output: 25
}

func ExampleRateLimits() {
	limits := cc.RateLimits{Primary: &cc.RateLimit{UsedPercent: 25, WindowMinutes: 300}}
	fmt.Println(limits.Primary.WindowMinutes)
	// Output: 300
}

func ExampleRateLimit() {
	limit := cc.RateLimit{UsedPercent: 25, WindowMinutes: 300, ResetsAt: 1790000000}
	fmt.Println(limit.UsedPercent, limit.WindowMinutes)
	// Output: 25 300
}
