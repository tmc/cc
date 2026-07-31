package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tmc/cc"
)

// entryWithTools builds a session Entry carrying the given tool_use blocks, the
// shape filesWrittenBy consumes. Message.Content is the raw JSON of a content
// block array, which Message.ContentBlocks unmarshals back.
func entryWithTools(blocks ...cc.ContentBlock) cc.Entry {
	content, _ := json.Marshal(blocks)
	return cc.Entry{Message: &cc.Message{Content: content}}
}

func toolUse(name, inputJSON string) cc.ContentBlock {
	return cc.ContentBlock{Type: "tool_use", Name: name, Input: []byte(inputJSON)}
}

func TestFilesWrittenByAcrossHarnesses(t *testing.T) {
	entries := []cc.Entry{
		// Claude Code.
		entryWithTools(toolUse("Write", `{"file_path":"/repo/a.go","content":"x"}`)),
		entryWithTools(toolUse("Edit", `{"file_path":"/repo/b.go"}`)),
		entryWithTools(toolUse("MultiEdit", `{"file_path":"/repo/c.go","edits":[]}`)),
		entryWithTools(toolUse("NotebookEdit", `{"notebook_path":"/repo/n.ipynb"}`)),
		// A read must never count as a write.
		entryWithTools(toolUse("Read", `{"file_path":"/repo/readonly.go"}`)),
		// opencode: normalized tool name, filePath key.
		entryWithTools(toolUse("Write", `{"filePath":"/repo/oc.go"}`)),
		// Antigravity: TargetFile key; multi_replace_file_content is not normalized.
		entryWithTools(toolUse("Write", `{"TargetFile":"/repo/ag1.go","Overwrite":true}`)),
		entryWithTools(toolUse("multi_replace_file_content", `{"TargetFile":"/repo/ag2.go"}`)),
		// codex apply_patch: path lives in the patch markers.
		entryWithTools(toolUse("apply_patch", `{"input":"*** Begin Patch\n*** Update File: /repo/patched.go\n@@\n-a\n+b\n*** End Patch"}`)),
		// codex shell heredoc write via Bash.
		entryWithTools(toolUse("Bash", `{"command":"cat >/repo/sh.go <<'EOF'\npackage main\nEOF"}`)),
	}

	got := filesWrittenBy(entries)
	want := []string{
		"/repo/a.go", "/repo/b.go", "/repo/c.go", "/repo/n.ipynb",
		"/repo/oc.go", "/repo/ag1.go", "/repo/ag2.go",
		"/repo/patched.go", "/repo/sh.go",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("filesWrittenBy across harnesses:\n got  %v\n want %v", got, want)
	}
	for _, f := range got {
		if f == "/repo/readonly.go" {
			t.Error("a Read was counted as a write")
		}
	}
}

func TestApplyPatchPaths(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Add File: new.go\n" +
		"*** Update File: pkg/edit.go\n" +
		"*** Delete File: old.go\n" +
		"*** End Patch"
	got := applyPatchPaths(patch)
	want := []string{"new.go", "pkg/edit.go", "old.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("applyPatchPaths = %v, want %v", got, want)
	}
	if applyPatchPaths("no markers here") != nil {
		t.Error("applyPatchPaths matched text with no markers")
	}
}

func TestBashWrittenFiles(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{"echo hi > out.txt", []string{"out.txt"}},
		{"go test ./... 2>&1 | tee log.txt", []string{"log.txt"}},
		{"cp src.go dst.go", []string{"dst.go"}},
		{"cat > gen.go <<'EOF'", []string{"gen.go"}},
		{"sed -i '' 's/a/b/' file.go", []string{"file.go"}},
		{"touch marker", []string{"marker"}},
		{"echo x 2>&1", nil},        // a redirect to a fd, not a file
		{"cat file.go", nil},        // a read
		{"grep foo *.go", nil},      // a read
		{"echo x > /dev/null", nil}, // devnull is filtered
	}
	for _, tt := range tests {
		if got := bashWrittenFiles(tt.cmd); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("bashWrittenFiles(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestPathMatches(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		// Substring (case-insensitive).
		{"main.go", "/repo/cmd/main.go", true},
		{"MAIN.GO", "/repo/cmd/main.go", true},
		{"running", "/repo/running.go", true},
		{"missing.go", "/repo/main.go", false},
		// Glob by basename.
		{"*.go", "/repo/a.go", true},
		{"*.go", "/repo/a.txt", false},
		{"*_test.go", "/repo/pkg/x_test.go", true},
		// Glob by full path.
		{"/repo/cmd/*.go", "/repo/cmd/main.go", true},
		{"/repo/cmd/*.go", "/repo/other/main.go", false},
		// Empty pattern never matches.
		{"", "/repo/a.go", false},
	}
	for _, tt := range tests {
		if got := pathMatches(tt.pattern, tt.path); got != tt.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestLiteralToken(t *testing.T) {
	tests := []struct {
		pattern, want string
	}{
		{"main.go", "main.go"},
		{"*.go", ".go"},
		{"cmd/*/main.go", "/main.go"},
		{"*_test.go", "_test.go"},
		{"*", ""},
	}
	for _, tt := range tests {
		if got := literalToken(tt.pattern); got != tt.want {
			t.Errorf("literalToken(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}
}
