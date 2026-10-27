package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
)

// isSubagentSession checks session relationships, not message parent IDs. Pi's
// parentId links messages within a session; NAC and Antigravity embed agent work
// in the parent transcript rather than exposing separate child sessions here.
func isSubagentSession(m cc.IndexEntry) bool {
	if m.IsSidechain {
		return true
	}
	switch cc.Harness(m.FullPath) {
	case "codex":
		h, ok := codexSessionHeader(m.FullPath)
		return ok && h.subagent
	case "opencode":
		var header struct {
			ParentID string `json:"parentID"`
		}
		data, err := os.ReadFile(m.FullPath)
		return err == nil && json.Unmarshal(data, &header) == nil && header.ParentID != ""
	case "claude", "gemini":
		if strings.HasPrefix(m.SessionID, "agent-") || filepath.Base(filepath.Dir(m.FullPath)) == "subagents" {
			return true
		}
		f, err := os.Open(m.FullPath)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), cc.MaxLineSize)
		for sc.Scan() {
			var header struct {
				IsSidechain *bool `json:"isSidechain"`
			}
			if json.Unmarshal(sc.Bytes(), &header) == nil && header.IsSidechain != nil {
				return *header.IsSidechain
			}
		}
	}
	return false
}
