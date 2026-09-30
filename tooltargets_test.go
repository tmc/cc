package cc

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBashWorkdir(t *testing.T) {
	for _, tt := range []struct{ name, input, want string }{
		{"exec", `{"cmd":"pwd","workdir":"/repo"}`, "/repo"},
		{"no directory", `{"cmd":"pwd"}`, ""},
		{"relative", `{"cmd":"pwd","workdir":"../repo"}`, "../repo"},
		{"shell", `{"command":["pwd"],"workdir":"/shell"}`, "/shell"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := "exec_command"
			if tt.name == "shell" {
				tool = "shell"
			}
			args, _ := json.Marshal(tt.input)
			line := `{"type":"response_item","payload":{"type":"function_call","name":"` + tool + `","call_id":"c","arguments":` + string(args) + `}}`
			entries, err := ReadAll(context.Background(), strings.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Message == nil {
				t.Fatalf("entries = %+v", entries)
			}
			calls := entries[0].Message.ToolUses()
			if len(calls) != 1 || calls[0].BashWorkdir() != tt.want || calls[0].BashCommand() != "pwd" {
				t.Fatalf("calls = %+v", calls)
			}
		})
	}
	for _, tt := range []struct {
		name  string
		block ContentBlock
	}{
		{"wrong tool", ContentBlock{Type: "tool_use", Name: "Read", Input: json.RawMessage(`{"workdir":"/repo"}`)}},
		{"wrong type", ContentBlock{Type: "tool_result", Name: "Bash", Input: json.RawMessage(`{"workdir":"/repo"}`)}},
		{"malformed", ContentBlock{Type: "tool_use", Name: "Bash", Input: json.RawMessage(`{`)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.block.BashWorkdir(); got != "" {
				t.Fatalf("workdir = %q", got)
			}
		})
	}
}

func TestApplyPatchPaths(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want       []string
	}{
		{"none", "ordinary text", nil},
		{"headers", "*** Begin Patch\n*** Add File: a.go\n*** Update File: b.go\n*** Delete File: c.go\n*** End Patch", []string{"a.go", "b.go", "c.go"}},
		{"move", "*** Update File: old.go\n*** Move to: new.go", []string{"old.go", "new.go"}},
		{"deduplicate", "*** Update File: a.go\n*** Update File: a.go", []string{"a.go"}},
		{"crlf and spaces", "*** Add File: path with spaces.go\r\n", []string{"path with spaces.go"}},
		{"empty", "*** Add File: \n", nil},
		{"body text", "+*** Add File: fake\nmentions *** Delete File: fake", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApplyPatchPaths(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("paths = %v, want %v", got, tt.want)
			}
		})
	}
}
