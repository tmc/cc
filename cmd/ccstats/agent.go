package main

import (
	"path/filepath"
	"strings"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

// agentSlugs maps a harness name to the slug cass indexes its sessions under.
// Codex is two products sharing one directory, told apart by the transcript.
var agentSlugs = map[string]string{
	"claude":      "claude-code",
	"gemini":      "gemini-cli",
	"codex":       "codex-cli",
	"opencode":    "opencode",
	"pi":          "pi",
	"antigravity": "antigravity",
}

// agentForFile names the harness a transcript came from, using the same slugs
// the index does. The token conventions differ between harnesses -- Codex
// counts cached and cache-written tokens inside its input figure, Claude Code
// alongside it -- so normalizing a file's usage requires knowing which one
// wrote it.
//
// The path decides, since every harness keeps its sessions under its own home;
// a file from somewhere else is read as Claude Code, which is the format
// [cc.ReadFile] assumes when nothing marks it otherwise.
func agentForFile(path string, entries []cc.Entry) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	for _, harness := range ccpaths.Harnesses {
		home, err := ccpaths.DataHome(harness)
		if err != nil {
			continue
		}
		if !under(abs, home) {
			continue
		}
		if harness == "codex" {
			return codexAgent(entries)
		}
		return agentSlugs[harness]
	}
	return "claude-code"
}

// under reports whether path is dir or sits beneath it.
func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// codexAgent tells the Codex CLI from the desktop app, which share a session
// directory and are separate harnesses in the index.
func codexAgent(entries []cc.Entry) string {
	for _, e := range entries {
		switch {
		case e.Originator == "codex_cli_rs" || e.Source == "cli":
			return "codex-cli"
		case e.Originator == "Codex Desktop" || e.Source == "vscode":
			return "codex-app"
		}
	}
	return "codex-cli"
}
