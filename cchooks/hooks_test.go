package cchooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatcherLooksLikeRule(t *testing.T) {
	tests := []struct {
		name    string
		matcher string
		want    bool
	}{
		{"permission rule", "Bash(it2 session send-text*)", true},
		{"permission rule with colon", "Bash(git status:*)", true},
		{"plain tool name", "Bash", false},
		{"alternation", "Write|Edit|Bash|Task", false},
		{"wildcard", "*", false},
		{"empty", "", false},
		{"leading group", "(Write|Edit)", false},
		{"anchored group", "^(Write)$", false},
		{"unclosed", "Bash(it2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matcherLooksLikeRule(tt.matcher); got != tt.want {
				t.Errorf("matcherLooksLikeRule(%q) = %v, want %v", tt.matcher, got, tt.want)
			}
		})
	}
}

// TestReadClaudeParked reads a settings file carrying both a live hook and a
// parked xhooks block, and checks that the parked handlers are reported but
// never presented as live.
func TestReadClaudeParked(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, filepath.Join("testdata", "settings-parked.json"), filepath.Join(dir, "settings.json"))

	handlers, diags := ReadClaude(dir, nil)
	if len(diags) != 0 {
		t.Fatalf("unexpected read diagnostics: %v", diags)
	}

	var live, parked int
	for _, h := range handlers {
		if h.Layer.Origin == Parked {
			parked++
		} else {
			live++
		}
	}
	if live != 1 {
		t.Errorf("live handlers = %d, want 1", live)
	}
	if parked != 2 {
		t.Errorf("parked handlers = %d, want 2", parked)
	}

	got := codes(Diagnose(handlers))
	for _, want := range []string{CodeParked, CodeMissing} {
		if !got[want] {
			t.Errorf("missing diagnostic %q; got %v", want, keys(got))
		}
	}
	// The parked handlers use $(...) but are never read by the agent, so they
	// must not also be reported as a substitution hazard.
	if got[CodeShellSubst] {
		t.Error("parked handlers should not produce a command-substitution diagnostic")
	}
}

// TestDiagnoseUnmatchable covers the failure this package exists to catch: a
// handler that installs cleanly, parses, and can never fire.
func TestDiagnoseUnmatchable(t *testing.T) {
	handlers := []Handler{{
		Layer:   Layer{Agent: "claude-code", Origin: Manual, Path: "settings.json"},
		Event:   "PreToolUse",
		Matcher: "Bash(it2 session send-text*)",
		Command: "agent-event",
		Args:    []string{"emit"},
	}}
	diags := Diagnose(handlers)
	if !codes(diags)[CodeUnmatchable] {
		t.Fatalf("want %q diagnostic, got %v", CodeUnmatchable, diags)
	}
	for _, d := range diags {
		if d.Code == CodeUnmatchable && d.Severity != "error" {
			t.Errorf("severity = %q, want error", d.Severity)
		}
	}
}

func TestDiagnoseDuplicateAcrossLayers(t *testing.T) {
	h := Handler{Event: "SessionStart", Command: "agent-event", Args: []string{"emit"}}
	a, b := h, h
	a.Layer = Layer{Agent: "claude-code", Origin: Manual, Path: "/u/settings.json"}
	b.Layer = Layer{Agent: "claude-code", Origin: Manual, Path: "/p/.claude/settings.json"}

	if !codes(Diagnose([]Handler{a, b}))[CodeDuplicate] {
		t.Error("want duplicate-handler diagnostic for the same handler in two layers")
	}
	if codes(Diagnose([]Handler{a}))[CodeDuplicate] {
		t.Error("a single handler must not be reported as a duplicate")
	}
}

// TestDiagnoseMisplacedIf covers the inverse of an unmatchable matcher. A
// rule-shaped matcher fails closed: the handler never runs. An if written on
// the matcher group instead of the handler fails open: it is ignored, and the
// handler runs on every call the matcher admits. Measured on claude 2.1.220,
// where a group-level if of Bash(ls:*) still ran against "echo probe456".
func TestDiagnoseMisplacedIf(t *testing.T) {
	h := Handler{
		Layer:   Layer{Agent: "claude-code", Origin: Manual, Path: "settings.json"},
		Event:   "PreToolUse",
		Matcher: "Bash",
		Command: "tap",
	}
	if codes(Diagnose([]Handler{h}))[CodeMisplacedIf] {
		t.Error("a handler with no group-level if must not be reported")
	}

	h.GroupIf = "Bash(it2 session send-text:*)"
	diags := Diagnose([]Handler{h})
	if !codes(diags)[CodeMisplacedIf] {
		t.Fatalf("want %q diagnostic, got %v", CodeMisplacedIf, diags)
	}
	for _, d := range diags {
		if d.Code == CodeMisplacedIf && d.Severity != "error" {
			t.Errorf("severity = %q, want error: the condition is silently dropped", d.Severity)
		}
	}
}

func TestDiagnoseCommandSubstitution(t *testing.T) {
	handlers := []Handler{{
		Layer:   Layer{Agent: "claude-code", Origin: Manual},
		Event:   "SessionStart",
		Command: "mkdir -p ~/.it2/sessions/$(it2 session current)",
	}}
	if !codes(Diagnose(handlers))[CodeShellSubst] {
		t.Error("want command-substitution diagnostic")
	}
}

// TestReadClaudePlugin covers the marketplace layout and ${CLAUDE_PLUGIN_ROOT}
// expansion; without expansion a plugin handler looks like a missing script.
func TestReadClaudePlugin(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, filepath.Join("testdata", "settings-parked.json"), filepath.Join(dir, "settings.json"))

	root := filepath.Join(dir, "plugins", "marketplaces", "mem-network")
	mkdirAll(t, filepath.Join(root, "hooks"))
	mkdirAll(t, filepath.Join(root, "scripts"))
	mkdirAll(t, filepath.Join(root, ".claude-plugin"))
	writeFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), `{"name": "memory"}`)
	writeFile(t, filepath.Join(root, "scripts", "on-start.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"), `{
	  "hooks": {"SessionStart": [{"hooks": [
	    {"type": "command", "command": "bash ${CLAUDE_PLUGIN_ROOT}/scripts/on-start.sh"}
	  ]}]}
	}`)

	handlers, _ := ReadClaude(dir, nil)
	var found *Handler
	for i := range handlers {
		if handlers[i].Layer.Origin == Plugin {
			found = &handlers[i]
		}
	}
	if found == nil {
		t.Fatal("plugin handler not found")
	}
	if strings.Contains(found.Command, "${CLAUDE_PLUGIN_ROOT}") {
		t.Errorf("CLAUDE_PLUGIN_ROOT not expanded: %s", found.Command)
	}
	want := filepath.Join(root, "scripts", "on-start.sh")
	if !strings.Contains(found.Command, want) {
		t.Errorf("command = %q, want it to contain %q", found.Command, want)
	}
	if found.Layer.Name != "memory@mem-network" {
		t.Errorf("plugin name = %q, want memory@mem-network", found.Layer.Name)
	}
	// lsp-only has no hooks and no recorded install path: silence, not a warning.
	for _, d := range Diagnose(handlers) {
		if d.Code == "plugin-unresolved" {
			t.Errorf("unexpected plugin-unresolved diagnostic: %s", d.Message)
		}
	}
}

// TestReadCodexMissingWrapper covers the shape the specs got wrong: Codex
// rejects a file whose event names sit at the root, and only warns about it.
func TestReadCodexMissingWrapper(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hooks.json"), `{
	  "SessionStart": [{"hooks": [{"type": "command", "command": "agent-event"}]}]
	}`)

	handlers, diags := ReadCodex(dir)
	if len(handlers) != 0 {
		t.Errorf("handlers = %d, want 0: an unwrapped file loads nothing", len(handlers))
	}
	if !codes(diags)["missing-hooks-wrapper"] {
		t.Fatalf("want missing-hooks-wrapper diagnostic, got %v", diags)
	}
}

func TestReadCodexWrapped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "hooks.json"), `{
	  "hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "agent-event"}]}]}
	}`)

	handlers, diags := ReadCodex(dir)
	if len(handlers) != 1 {
		t.Fatalf("handlers = %d, want 1", len(handlers))
	}
	if handlers[0].Layer.Agent != "codex" || handlers[0].Event != "SessionStart" {
		t.Errorf("handler = %+v", handlers[0])
	}
	if codes(diags)["missing-hooks-wrapper"] {
		t.Error("a correctly wrapped file must not report the wrapper diagnostic")
	}
}

func TestReadCodexAbsent(t *testing.T) {
	handlers, diags := ReadCodex(t.TempDir())
	if len(handlers) != 0 || len(diags) != 0 {
		t.Errorf("an uninstalled agent must be silent; got %d handlers, %v", len(handlers), diags)
	}
}

func TestWorst(t *testing.T) {
	tests := []struct {
		name  string
		diags []Diagnostic
		want  string
	}{
		{"none", nil, ""},
		{"info", []Diagnostic{{Severity: "info"}}, "info"},
		{"warn beats info", []Diagnostic{{Severity: "info"}, {Severity: "warn"}}, "warn"},
		{"error beats all", []Diagnostic{{Severity: "warn"}, {Severity: "error"}}, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Worst(tt.diags); got != tt.want {
				t.Errorf("Worst() = %q, want %q", got, tt.want)
			}
		})
	}
}

func codes(diags []Diagnostic) map[string]bool {
	m := map[string]bool{}
	for _, d := range diags {
		m[d.Code] = true
	}
	return m
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dst, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestReadClaudePluginUninstalled covers an enabled plugin whose recorded
// installPath is gone while an identical marketplace checkout remains beside
// it. The agent refuses to load the plugin in that state, so the leftover copy
// must not be reported as live no matter how readable it is.
func TestReadClaudePluginUninstalled(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, filepath.Join("testdata", "settings-parked.json"), filepath.Join(dir, "settings.json"))

	plugins := filepath.Join(dir, "plugins")
	mkdirAll(t, plugins)
	writeFile(t, filepath.Join(plugins, "installed_plugins.json"), `{
	  "version": 2,
	  "plugins": {"memory@mem-network": [
	    {"installPath": "`+filepath.Join(plugins, "cache", "mem-network", "memory", "0.1.0")+`"}
	  ]}
	}`)

	// The recorded installPath is deliberately never created; only a
	// marketplace checkout of the same plugin exists.
	root := filepath.Join(plugins, "marketplaces", "mem-network")
	mkdirAll(t, filepath.Join(root, "hooks"))
	mkdirAll(t, filepath.Join(root, "scripts"))
	mkdirAll(t, filepath.Join(root, ".claude-plugin"))
	writeFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), `{"name": "memory"}`)
	writeFile(t, filepath.Join(root, "scripts", "on-start.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "hooks", "hooks.json"), `{
	  "hooks": {"SessionStart": [{"hooks": [
	    {"type": "command", "command": "bash ${CLAUDE_PLUGIN_ROOT}/scripts/on-start.sh"}
	  ]}]}
	}`)

	handlers, diags := ReadClaude(dir, nil)
	var live, dead int
	for _, h := range handlers {
		switch h.Layer.Origin {
		case Plugin:
			live++
		case Unresolved:
			dead++
		}
	}
	if live != 0 {
		t.Errorf("live plugin handlers = %d, want 0: an uninstalled plugin runs nothing", live)
	}
	if dead != 1 {
		t.Errorf("unresolved handlers = %d, want 1: the leftover copy is still worth showing", dead)
	}
	if !codes(diags)[CodeUnresolved] {
		t.Errorf("want a %s diagnostic", CodeUnresolved)
	}
	for _, d := range diags {
		if d.Code == CodeUnresolved && d.Severity != "error" {
			t.Errorf("%s severity = %q, want error: the hooks do not run", d.Code, d.Severity)
		}
	}
}
