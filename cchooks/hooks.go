package cchooks

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Origin says which kind of configuration layer a handler came from. Layers
// with a lower value are read first; agents merge later layers into earlier
// ones rather than replacing them.
type Origin int

// Origins, in the order agents read them. The last two do not run.
const (
	Managed    Origin = iota // system policy, not user-editable
	Manual                   // hand-written in a user or project settings file
	Plugin                   // installed by an enabled plugin
	Parked                   // a disabled-by-rename block: xhooks, hooks.bak
	Unresolved               // an enabled plugin that is no longer installed
)

var originNames = [...]string{"managed", "manual", "plugin", "parked", "unresolved"}

// Runs reports whether an agent will execute handlers from this origin. Parked
// and Unresolved handlers exist on disk and are reported so that they can be
// seen and removed, but nothing runs them.
func (o Origin) Runs() bool { return o != Parked && o != Unresolved }

func (o Origin) String() string {
	if o < 0 || int(o) >= len(originNames) {
		return "unknown"
	}
	return originNames[o]
}

// Layer is one configuration file that can contribute handlers.
type Layer struct {
	Agent  string // "claude-code" or "codex"
	Origin Origin
	Path   string // the file read
	Name   string // plugin name, for Origin Plugin
	Scope  string // "user", "project", "local", or "" when not applicable
}

// Handler is one hook command as installed. Command is expanded: plugin
// handlers have ${CLAUDE_PLUGIN_ROOT} resolved, so Command is what will run.
type Handler struct {
	Layer   Layer
	Event   string // "PreToolUse", "SessionStart", ...
	Matcher string // empty when the group declares none
	If      string // permission-rule condition, Codex and newer Claude schemas
	Type    string // "command"
	Command string
	Args    []string
	Timeout int  // seconds; 0 when unset
	Async   bool // handler does not block the agent
}

// script returns the executable the handler runs, or "" when the command is a
// shell string whose program cannot be determined without a shell parser.
func (h Handler) script() string {
	if len(h.Args) > 0 || !strings.ContainsAny(h.Command, " \t|;&$><") {
		return h.Command
	}
	// A bare "bash /path/script.sh" is common enough to be worth resolving.
	fields := strings.Fields(h.Command)
	if len(fields) == 2 && (fields[0] == "bash" || fields[0] == "sh") {
		return fields[1]
	}
	return ""
}

// Diagnostic is one health finding. Code is stable and machine-readable;
// Message explains it for a person.
type Diagnostic struct {
	Code     string // stable identifier, e.g. "unmatchable-matcher"
	Severity string // "error", "warn", or "info"
	Message  string
	Handler  *Handler // nil for findings about a layer as a whole
	Layer    Layer
}

// Diagnostic codes.
const (
	CodeParked      = "parked"
	CodeUnresolved  = "plugin-unresolved"
	CodeMissing     = "missing-script"
	CodeNotExec     = "script-not-executable"
	CodeUnmatchable = "unmatchable-matcher"
	CodeSlowShell   = "per-event-subprocess"
	CodeNoTimeout   = "no-timeout"
	CodeDuplicate   = "duplicate-handler"
	CodeShellSubst  = "command-substitution"
)

// matcherLooksLikeRule reports whether a matcher is written in permission-rule
// syntax, e.g. "Bash(it2 session send-text*)". Agents compile a matcher as a
// regular expression and test it against the tool name alone, so a rule-shaped
// matcher never matches and the handler never runs. The condition belongs in
// the "if" field instead.
func matcherLooksLikeRule(m string) bool {
	i := strings.IndexByte(m, '(')
	if i <= 0 || !strings.HasSuffix(m, ")") {
		return false
	}
	// A leading group or alternation such as "(Write|Edit)" is a real regexp.
	return !strings.ContainsAny(m[:i], "|)^$[]")
}

// substPattern matches shell command substitution, the construct that turns an
// identity-resolution failure into a corrupt path.
var substPattern = regexp.MustCompile(`\$\(|` + "`")

// Diagnose reports the problems it can see in handlers without running them.
// It reports duplicates across the whole set, so callers should pass every
// handler from every layer at once.
func Diagnose(handlers []Handler) []Diagnostic {
	var out []Diagnostic
	seen := map[string][]Handler{}
	parked := map[string]int{}
	for i := range handlers {
		h := handlers[i]
		add := func(code, sev, msg string) {
			out = append(out, Diagnostic{Code: code, Severity: sev, Message: msg, Handler: &handlers[i], Layer: h.Layer})
		}
		// A parked block is one fact about a file, not one per handler in it.
		if h.Layer.Origin == Parked {
			parked[h.Layer.Path]++
			continue
		}
		// Handlers that cannot run need no lint; their origin is the finding,
		// and it is reported once by the reader that resolved them.
		if !h.Layer.Origin.Runs() {
			continue
		}
		if matcherLooksLikeRule(h.Matcher) {
			add(CodeUnmatchable, "error", "matcher is permission-rule syntax but is compiled as a regexp against the tool name; it can never match. Use the if field.")
		}
		if s := h.script(); s != "" && filepath.IsAbs(s) {
			switch info, err := os.Stat(s); {
			case err != nil:
				add(CodeMissing, "error", "handler script does not exist: "+s)
			case info.Mode()&0o111 == 0 && !strings.HasPrefix(h.Command, "bash ") && !strings.HasPrefix(h.Command, "sh "):
				add(CodeNotExec, "error", "handler script is not executable: "+s)
			}
		}
		if substPattern.MatchString(h.Command) {
			add(CodeShellSubst, "error", "command substitution in a handler: a resolution failure becomes an empty path segment")
		}
		if isPerEvent(h.Event) && mentionsSlowCommand(h.Command) {
			add(CodeSlowShell, "warn", "shells out to a terminal command on a per-event hook; resolve identity once at session start")
		}
		if h.Timeout == 0 && !h.Async {
			add(CodeNoTimeout, "info", "no timeout set; the agent's default applies")
		}
		k := h.Event + "\x00" + h.Matcher + "\x00" + h.Command + "\x00" + strings.Join(h.Args, "\x00")
		seen[k] = append(seen[k], h)
	}
	paths := make([]string, 0, len(parked))
	for p := range parked {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		out = append(out, Diagnostic{
			Code:     CodeParked,
			Severity: "warn",
			Message:  fmt.Sprintf("%d handlers sit in a disabled-by-rename block; the agent never reads them", parked[p]),
			Layer:    Layer{Agent: "claude-code", Origin: Parked, Path: p},
		})
	}
	for _, group := range seen {
		if len(group) < 2 {
			continue
		}
		var where []string
		for _, h := range group {
			where = append(where, h.Layer.Path)
		}
		sort.Strings(where)
		out = append(out, Diagnostic{
			Code:     CodeDuplicate,
			Severity: "warn",
			Message:  "same handler installed in " + strings.Join(where, " and ") + "; it runs once per layer",
			Layer:    group[0].Layer,
		})
	}
	return out
}

// isPerEvent reports whether an event fires often enough that a subprocess in
// its handler is a latency hazard.
func isPerEvent(event string) bool {
	switch event {
	case "PreToolUse", "PostToolUse", "UserPromptSubmit":
		return true
	}
	return false
}

func mentionsSlowCommand(cmd string) bool {
	for _, c := range []string{"it2 ", "osascript", "tmux "} {
		if strings.Contains(cmd, c) {
			return true
		}
	}
	return false
}
