package cchooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// codexHooks is the root of ~/.codex/hooks.json. Codex requires the hooks
// wrapper and rejects unknown keys at the root, so a file whose event names sit
// at the root is not merely ignored: it fails to parse and every hook in it is
// dropped, with a warning rather than an error.
type codexHooks struct {
	Description string                   `json:"description"`
	Hooks       map[string][]claudeGroup `json:"hooks"`
}

// ReadCodex returns the handlers Codex would load from codexDir. It reports the
// wrapper mistake explicitly, because Codex itself only warns about it.
func ReadCodex(codexDir string) ([]Handler, []Diagnostic) {
	var handlers []Handler
	var diags []Diagnostic

	path := filepath.Join(codexDir, "hooks.json")
	layer := Layer{Agent: "codex", Origin: Manual, Path: path, Scope: "user"}
	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// Not installed is not a fault.
	case err != nil:
		diags = append(diags, Diagnostic{Code: "unreadable-layer", Severity: "error",
			Message: err.Error(), Layer: layer})
	default:
		var root codexHooks
		if err := json.Unmarshal(data, &root); err != nil {
			diags = append(diags, Diagnostic{Code: "malformed-layer", Severity: "error",
				Message: fmt.Sprintf("parse %s: %v", path, err), Layer: layer})
			break
		}
		if root.Hooks == nil {
			if events := unwrappedEvents(data); len(events) > 0 {
				diags = append(diags, Diagnostic{Code: "missing-hooks-wrapper", Severity: "error",
					Message: "event names are at the file root (" + strings.Join(events, ", ") +
						"); Codex requires a top-level \"hooks\" object and rejects unknown root keys, so every hook here is dropped with only a warning",
					Layer: layer})
			}
			break
		}
		handlers = append(handlers, flattenClaude(root.Hooks, layer)...)
	}

	diags = append(diags, codexConfigDiagnostics(codexDir)...)
	return handlers, diags
}

// claudeEventNames are the event names a misplaced Codex hooks file is most
// likely to carry at its root.
var claudeEventNames = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "UserPromptSubmit": true,
	"Stop": true, "SessionStart": true, "SessionEnd": true,
	"PreCompact": true, "PostCompact": true, "SubagentStop": true,
	"PermissionRequest": true,
}

func unwrappedEvents(data []byte) []string {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	var out []string
	for k := range raw {
		if claudeEventNames[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

var (
	tomlHooksTable = regexp.MustCompile(`(?m)^\s*\[hooks[.\]]`)
	tomlTrustState = regexp.MustCompile(`(?m)^\s*\[hooks\.state\b`)
)

// codexConfigDiagnostics reports what config.toml carries without parsing TOML.
// Codex accepts inline [hooks] tables there, and records the trust decision for
// each handler under [hooks.state]; both change whether a handler runs, so
// their presence is worth reporting even when their contents are not read.
func codexConfigDiagnostics(codexDir string) []Diagnostic {
	path := filepath.Join(codexDir, "config.toml")
	layer := Layer{Agent: "codex", Origin: Manual, Path: path, Scope: "user"}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Diagnostic
	if tomlHooksTable.Match(data) {
		out = append(out, Diagnostic{Code: "codex-config-hooks", Severity: "info",
			Message: "config.toml declares [hooks] tables; this package does not parse TOML, so those handlers are not listed",
			Layer:   layer})
	}
	if !tomlTrustState.Match(data) {
		out = append(out, Diagnostic{Code: "codex-untrusted", Severity: "warn",
			Message: "no [hooks.state] trust records; Codex gates every handler behind a content-hash review, so installed hooks stay inert until it is granted",
			Layer:   layer})
	}
	return out
}
