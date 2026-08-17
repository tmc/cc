package cchooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// claudeSettings is the part of a Claude Code settings file this package
// reads. Everything else in the file is preserved by not being touched: this
// package never writes.
type claudeSettings struct {
	Hooks          map[string][]claudeGroup `json:"hooks"`
	EnabledPlugins map[string]bool          `json:"enabledPlugins"`
}

// claudeGroup is one matcher group under an event name. If is not a field of a
// group; it is read only to report that someone put it there, because an agent
// ignores it silently and every handler in the group then runs unconditionally.
type claudeGroup struct {
	Matcher string          `json:"matcher"`
	If      string          `json:"if"`
	Hooks   []claudeHandler `json:"hooks"`
}

type claudeHandler struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	If      string   `json:"if"`
	Timeout int      `json:"timeout"`
	Async   bool     `json:"async"`
}

// parkedKeys are the top-level names a disabled hooks block is renamed to. The
// agent ignores them, which is the point; reporting them is how a parked block
// stops being invisible.
var parkedKeys = []string{"xhooks", "hooks.bak", "hooks_old", "_hooks", "hooks.disabled"}

// ReadClaude returns every handler Claude Code would load, from every layer,
// plus the parked blocks it would ignore. claudeDir is the user configuration
// directory (typically ~/.claude); projectDirs are working directories whose
// .claude/settings.json should be included. Missing files are not an error;
// unreadable ones are reported as diagnostics by [Read].
func ReadClaude(claudeDir string, projectDirs []string) ([]Handler, []Diagnostic) {
	var handlers []Handler
	var diags []Diagnostic

	read := func(path string, origin Origin, scope, name string) *claudeSettings {
		layer := Layer{Agent: "claude-code", Origin: origin, Path: path, Scope: scope, Name: name}
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				diags = append(diags, Diagnostic{Code: "unreadable-layer", Severity: "error",
					Message: err.Error(), Layer: layer})
			}
			return nil
		}
		var s claudeSettings
		if err := json.Unmarshal(data, &s); err != nil {
			diags = append(diags, Diagnostic{Code: "malformed-layer", Severity: "error",
				Message: fmt.Sprintf("parse %s: %v", path, err), Layer: layer})
			return nil
		}
		handlers = append(handlers, flattenClaude(s.Hooks, layer)...)
		handlers = append(handlers, readParked(data, layer)...)
		return &s
	}

	for _, p := range managedSettingsPaths() {
		read(p, Managed, "", "")
	}
	user := read(filepath.Join(claudeDir, "settings.json"), Manual, "user", "")
	read(filepath.Join(claudeDir, "settings.local.json"), Manual, "local", "")
	for _, dir := range projectDirs {
		read(filepath.Join(dir, ".claude", "settings.json"), Manual, "project", "")
		read(filepath.Join(dir, ".claude", "settings.local.json"), Manual, "local", "")
	}
	if user != nil {
		files, missing := pluginHookFiles(claudeDir, user.EnabledPlugins)
		for _, p := range files {
			origin := Plugin
			if p.dead {
				origin = Unresolved
			}
			read(p.path, origin, "", p.name)
		}
		for _, name := range missing {
			diags = append(diags, Diagnostic{Code: CodeUnresolved, Severity: "error",
				Message: "plugin " + name + " is enabled but not installed at its recorded installPath; the agent refuses to load it, so none of its hooks run. Reinstall it with /plugin, or disable it to stop the error.",
				Layer:   Layer{Agent: "claude-code", Origin: Unresolved, Name: name, Path: claudeDir}})
		}
	}
	return handlers, diags
}

// readParked returns the handlers inside disabled-by-rename blocks. They never
// run; they are reported so that rot is visible and removable.
func readParked(data []byte, layer Layer) []Handler {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var out []Handler
	for _, key := range parkedKeys {
		blob, ok := raw[key]
		if !ok {
			continue
		}
		var hooks map[string][]claudeGroup
		if err := json.Unmarshal(blob, &hooks); err != nil {
			continue
		}
		l := layer
		l.Origin = Parked
		out = append(out, flattenClaude(hooks, l)...)
	}
	return out
}

func flattenClaude(hooks map[string][]claudeGroup, layer Layer) []Handler {
	events := make([]string, 0, len(hooks))
	for e := range hooks {
		events = append(events, e)
	}
	sort.Strings(events)

	var out []Handler
	for _, event := range events {
		for _, g := range hooks[event] {
			for _, h := range g.Hooks {
				out = append(out, Handler{
					Layer:   layer,
					Event:   event,
					Matcher: g.Matcher,
					If:      h.If,
					GroupIf: g.If,
					Type:    h.Type,
					Command: expandPluginRoot(h.Command, layer),
					Args:    h.Args,
					Timeout: h.Timeout,
					Async:   h.Async,
				})
			}
		}
	}
	return out
}

// expandPluginRoot resolves ${CLAUDE_PLUGIN_ROOT}, which plugin hook files use
// to refer to their own installation directory. Without this a plugin handler
// looks like a missing script.
func expandPluginRoot(cmd string, layer Layer) string {
	if layer.Origin != Plugin && layer.Origin != Unresolved {
		return cmd
	}
	if !strings.Contains(cmd, "${CLAUDE_PLUGIN_ROOT}") {
		return cmd
	}
	// The hooks file lives at <root>/hooks/hooks.json.
	root := filepath.Dir(filepath.Dir(layer.Path))
	return strings.ReplaceAll(cmd, "${CLAUDE_PLUGIN_ROOT}", root)
}

type pluginHooks struct {
	name string
	path string
	dead bool // found, but the plugin is not installed, so nothing runs it
}

// pluginHookFiles returns the hooks file of every enabled plugin, and the names
// of enabled plugins that are enabled but not installed.
//
// Keys of enabled are "<plugin>@<marketplace>". The installPath recorded in
// installed_plugins.json is authoritative: when that directory is gone the
// agent refuses to load the plugin ("Plugin directory does not exist ... run
// /plugin to reinstall") even though a marketplace checkout of the same
// plugin, with the same scripts, is sitting beside it. So the recorded path is
// tried first, and a marketplace copy is a fallback only for a plugin with no
// record at all. A copy found while the record is broken is reported, since
// knowing what a dead plugin used to run is the point of asking, but it is
// returned as missing so the caller can mark it Unresolved rather than live.
//
// A plugin with no hooks file anywhere is not reported: most plugins have none.
func pluginHookFiles(claudeDir string, enabled map[string]bool) (files []pluginHooks, missing []string) {
	names := make([]string, 0, len(enabled))
	for k, on := range enabled {
		if on {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	root := filepath.Join(claudeDir, "plugins")
	for _, key := range names {
		plugin, market, ok := strings.Cut(key, "@")
		if !ok {
			continue
		}
		// Anywhere a copy can sit. The recorded installation, when there is
		// one, decides which of them the agent will actually load.
		anywhere := []string{
			filepath.Join(root, "cache", market, plugin, "*", "hooks", "hooks.json"),
			filepath.Join(root, "marketplaces", market, "plugins", plugin, "hooks", "hooks.json"),
		}
		// A single-plugin marketplace keeps its hooks at the checkout root,
		// where the path names the marketplace and not the plugin. Only the
		// plugin its manifest names may claim them, or every enabled plugin
		// in the marketplace would inherit the same handlers.
		if marketRoot := filepath.Join(root, "marketplaces", market); pluginManifestName(marketRoot) == plugin {
			anywhere = append(anywhere, filepath.Join(marketRoot, "hooks", "hooks.json"))
		}
		// With no record, nothing says which copy is authoritative, so take
		// any of them at face value rather than inventing a verdict.
		globs, installed := anywhere, true
		if p := recordedInstallPath(root, key); p != "" {
			if _, err := os.Stat(p); err == nil {
				globs = []string{filepath.Join(p, "hooks", "hooks.json")}
			} else {
				// Uninstalled. A leftover copy is reported only to show what
				// the plugin used to run.
				installed = false
				missing = append(missing, key)
			}
		}
		for _, g := range globs {
			matches, err := filepath.Glob(g)
			if err != nil {
				continue
			}
			sort.Strings(matches)
			for _, m := range matches {
				files = append(files, pluginHooks{name: key, path: m, dead: !installed})
			}
			if len(matches) > 0 {
				break
			}
		}
	}
	return files, missing
}

// pluginManifestName returns the name in a plugin checkout's manifest, or ""
// when the directory is not a plugin root.
func pluginManifestName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.Name
}

// recordedInstallPath returns the installPath installed_plugins.json records
// for a plugin, or "" when there is no record. A recorded path that no longer
// exists is how a plugin's hooks go missing without anything reporting it.
func recordedInstallPath(pluginRoot, key string) string {
	data, err := os.ReadFile(filepath.Join(pluginRoot, "installed_plugins.json"))
	if err != nil {
		return ""
	}
	var rec struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if json.Unmarshal(data, &rec) != nil {
		return ""
	}
	for _, e := range rec.Plugins[key] {
		if e.InstallPath != "" {
			return e.InstallPath
		}
	}
	return ""
}
