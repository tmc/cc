package cchooks

import (
	"runtime"

	"github.com/tmc/cc/ccpaths"
)

// Report is the result of reading every layer of every agent.
type Report struct {
	Handlers    []Handler
	Diagnostics []Diagnostic
}

// Read reads the hook configuration of every agent found on this machine.
// projectDirs are working directories whose .claude/settings.json files should
// be included; pass nil for user-level configuration only.
//
// An agent that is not installed contributes nothing and is not an error.
func Read(projectDirs []string) (*Report, error) {
	r := &Report{}
	if dir, err := ccpaths.ClaudeHome(); err == nil {
		h, d := ReadClaude(dir, projectDirs)
		r.Handlers = append(r.Handlers, h...)
		r.Diagnostics = append(r.Diagnostics, d...)
	}
	if dir, err := ccpaths.CodexHome(); err == nil {
		h, d := ReadCodex(dir)
		r.Handlers = append(r.Handlers, h...)
		r.Diagnostics = append(r.Diagnostics, d...)
	}
	r.Diagnostics = append(r.Diagnostics, Diagnose(r.Handlers)...)
	return r, nil
}

// Worst returns the most severe severity among the diagnostics, or "" when
// there are none. Callers use it to choose an exit status.
func Worst(diags []Diagnostic) string {
	worst := ""
	for _, d := range diags {
		switch d.Severity {
		case "error":
			return "error"
		case "warn":
			worst = "warn"
		case "info":
			if worst == "" {
				worst = "info"
			}
		}
	}
	return worst
}

// managedSettingsPaths returns the system policy files that install hooks no
// user layer can remove. There are none outside macOS today.
func managedSettingsPaths() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
}
