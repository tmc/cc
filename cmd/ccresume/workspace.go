package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
)

// matchingWorkspace resolves a directory query from recorded working directories
// and structured tool paths. Prose and command output are not workspace evidence.
func matchingWorkspace(query, path string) string {
	query = filepath.Clean(query)
	if !strings.Contains(query, string(filepath.Separator)) || query == "." {
		return ""
	}
	entries, err := cc.ReadFile(context.Background(), path)
	if err != nil {
		return ""
	}
	var target string
	consider := func(path string) {
		if !filepath.IsAbs(path) {
			return
		}
		for dir := filepath.Clean(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
			if (strings.EqualFold(dir, query) || strings.HasSuffix(strings.ToLower(dir), string(filepath.Separator)+strings.ToLower(query))) && dirExists(dir) && !isAgentStoragePath(dir) {
				target = dir
				return
			}
		}
	}
	for _, e := range entries {
		consider(e.CWD)
		if e.Message == nil {
			continue
		}
		for _, tool := range e.Message.ToolUses() {
			var input map[string]json.RawMessage
			if json.Unmarshal(tool.Input, &input) != nil {
				continue
			}
			for _, key := range []string{"cwd", "Cwd", "workdir", "file_path", "filePath", "path", "Path", "TargetFile", "AbsolutePath", "DirectoryPath"} {
				var value string
				if json.Unmarshal(input[key], &value) == nil {
					consider(value)
				}
			}
		}
	}
	return target
}
