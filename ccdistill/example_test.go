package ccdistill_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccdistill"
)

// session builds a minimal two-turn trajectory: a human prompt, an assistant
// reply with a Bash tool call, the tool result, and a final assistant line.
func session() []cc.Entry {
	user := func(text string) cc.Entry {
		return cc.Entry{Type: "user", Message: &cc.Message{Role: "user", Content: mustJSON(text)}}
	}
	asst := func(blocks ...any) cc.Entry {
		return cc.Entry{Type: "assistant", Message: &cc.Message{Role: "assistant", Content: mustJSON(blocks)}}
	}
	toolResult := func(id, content string) cc.Entry {
		block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
		return cc.Entry{Type: "user", Message: &cc.Message{Role: "user", Content: mustJSON([]any{block})}}
	}
	return []cc.Entry{
		user("run the tests"),
		asst(
			map[string]any{"type": "text", "text": "Running them now."},
			map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		),
		toolResult("t1", "ok"),
		asst(map[string]any{"type": "text", "text": "Tests pass."}),
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Example shows the end-to-end data-prep flow: read a session, transform it
// into a Sample, redact secrets, and encode one NDJSON training record.
func Example() {
	s, err := ccdistill.Transform(session(), ccdistill.Options{})
	if err != nil {
		fmt.Println("transform:", err)
		return
	}
	ccdistill.Redact(s)

	enc, _ := ccdistill.NewEncoder(ccdistill.FormatAnthropic)
	enc.Encode(os.Stdout, s)
	fmt.Println()
	// Output:
	// {"messages":[{"role":"user","content":[{"type":"text","text":"run the tests"}]},{"role":"assistant","content":[{"type":"text","text":"Running them now."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./..."}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},{"role":"assistant","content":[{"type":"text","text":"Tests pass."}]}],"meta":{"session_id":"","source":"","first_prompt":"run the tests","num_turns":4,"num_tool_uses":1}}
}

// ExampleSignalFilter shows dropping a tool-free chat when at least one tool
// call is required.
func ExampleSignalFilter() {
	chat := []cc.Entry{
		{Type: "user", Message: &cc.Message{Role: "user", Content: mustJSON("hello")}},
		{Type: "assistant", Message: &cc.Message{Role: "assistant", Content: mustJSON([]any{map[string]any{"type": "text", "text": "hi"}})}},
	}
	s, _ := ccdistill.Transform(chat, ccdistill.Options{})
	fmt.Println("keep with min 0 tool uses:", ccdistill.SignalFilter(0)(s))
	fmt.Println("keep with min 1 tool use: ", ccdistill.SignalFilter(1)(s))
	// Output:
	// keep with min 0 tool uses: true
	// keep with min 1 tool use:  false
}

func ExampleDecodeAnthropic() {
	record := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"main.go"}}]}],"meta":{"session_id":"abc"}}`
	sample, err := ccdistill.DecodeAnthropic(strings.NewReader(record))
	if err != nil {
		fmt.Println(err)
		return
	}
	call := sample.Turns[0].Blocks[0].ToolUse
	fmt.Println(sample.Meta.SessionID, call.ID, call.Name)
	// Output:
	// abc t1 Read
}
