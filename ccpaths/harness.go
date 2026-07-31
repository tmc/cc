package ccpaths

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Harnesses lists the coding-agent harness names understood by DataHome,
// SessionsRoot, and ProjectDir.
var Harnesses = []string{"claude", "gemini", "codex", "opencode", "pi", "antigravity"}

// DataHome returns the base data directory for the named harness.
func DataHome(harness string) (string, error) {
	switch harness {
	case "claude":
		return ClaudeHome()
	case "gemini":
		return GeminiHome()
	case "codex":
		return CodexHome()
	case "opencode":
		return OpenCodeHome()
	case "pi":
		return PiHome()
	case "antigravity":
		return AntigravityHome()
	}
	return "", fmt.Errorf("unknown harness %q (want one of %s)", harness, strings.Join(Harnesses, ", "))
}

// SessionsRoot returns the directory under which the named harness stores
// session data: the tree a search or index would walk.
func SessionsRoot(harness string) (string, error) {
	home, err := DataHome(harness)
	if err != nil {
		return "", err
	}
	switch harness {
	case "claude", "gemini":
		return filepath.Join(home, "projects"), nil
	case "codex", "pi":
		return filepath.Join(home, "sessions"), nil
	case "opencode":
		return filepath.Join(home, "storage", "session"), nil
	case "antigravity":
		return filepath.Join(home, "brain"), nil
	}
	return "", fmt.Errorf("unknown harness %q", harness)
}

// ProjectDir returns the named harness's per-project data directory for dir,
// which must be absolute. Claude Code and Gemini CLI keep
// projects/<encoded-path>; pi keeps sessions/<encoded-cwd>. The other
// harnesses organize session data by date, opaque ID, or conversation rather
// than by project, so ProjectDir returns an error for them; use SessionsRoot.
func ProjectDir(harness, dir string) (string, error) {
	home, err := DataHome(harness)
	if err != nil {
		return "", err
	}
	switch harness {
	case "claude", "gemini":
		return filepath.Join(home, "projects", EncodePath(dir)), nil
	case "pi":
		return filepath.Join(home, "sessions", PiEncodePath(dir)), nil
	case "codex", "opencode", "antigravity":
		return "", fmt.Errorf("harness %q has no per-project directory (sessions are organized by date or ID; use the sessions root)", harness)
	}
	return "", fmt.Errorf("unknown harness %q", harness)
}

// PiEncodePath converts a filesystem path to pi's session-directory encoding:
// "/" is replaced with "-" (dots are preserved, unlike Claude Code's
// EncodePath) and the result is wrapped in an extra leading and trailing "-",
// so "/private/tmp" encodes as "--private-tmp--".
func PiEncodePath(path string) string {
	return "-" + strings.ReplaceAll(path, "/", "-") + "--"
}
