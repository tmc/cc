package cc

import (
	"os"
	"path/filepath"
)

// ClaudeHome returns the base directory for Claude data.
// It uses CLAUDE_HOME if set, otherwise defaults to ~/.claude.
func ClaudeHome() (string, error) {
	if h := os.Getenv("CLAUDE_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// GeminiHome returns the base directory for Gemini CLI data.
// It uses GEMINI_HOME if set, otherwise defaults to ~/.gemini.
func GeminiHome() (string, error) {
	if h := os.Getenv("GEMINI_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gemini"), nil
}

// CodexHome returns the base directory for Codex data.
// It uses CODEX_HOME if set, otherwise defaults to ~/.codex.
func CodexHome() (string, error) {
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// DecodeSegments tries all possible decodings of dash-separated path segments
// and returns the first existing path.
func DecodeSegments(current string, remaining []string) (string, bool) {
	if len(remaining) == 0 {
		_, err := os.Stat(current)
		return current, err == nil
	}

	seg := remaining[0]
	rest := remaining[1:]

	// Try "/" (path separator), "." (dot in name), "-" (literal dash).
	// For each, recurse and return the first result that resolves to a real path.
	for _, sep := range []string{"/", ".", "-"} {
		candidate := current + sep + seg
		if result, ok := DecodeSegments(candidate, rest); ok {
			return result, true
		}
	}

	return "", false
}
