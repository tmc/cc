package ccpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// EncodePath converts a filesystem path to Claude Code's project-directory
// encoding: "/" and "." are both replaced with "-".
//
//	/Volumes/tmc/go/src/github.com/tmc/cc
//	→ -Volumes-tmc-go-src-github-com-tmc-cc
func EncodePath(path string) string {
	s := strings.ReplaceAll(path, string(filepath.Separator), "-")
	return strings.ReplaceAll(s, ".", "-")
}

// ShortPath returns p with the current user's home directory replaced by "~".
func ShortPath(p string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// ParseDuration parses a time duration, accepting d and w suffixes in addition
// to the standard library syntax.
func ParseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return time.ParseDuration(s)
	}
	switch suffix {
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(num) * 7 * 24 * time.Hour, nil
	default:
		return time.ParseDuration(s)
	}
}

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

// OpenCodeHome returns the base directory for opencode data.
// It uses OPENCODE_HOME if set, otherwise defaults to ~/.local/share/opencode.
func OpenCodeHome() (string, error) {
	if h := os.Getenv("OPENCODE_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}

// DecodeSegments tries all possible decodings of dash-separated path segments
// and returns the first existing path.
//
// Claude Code encodes paths by replacing "/" and "." with "-". This means
// "--" in the encoded form represents a separator dash followed by a dot,
// which appears as an empty segment when split on "-". When we encounter
// an empty segment, we consume the next segment and prefix it with "."
// (e.g., "--codex" decodes to "/.codex").
func DecodeSegments(current string, remaining []string) (string, bool) {
	if len(remaining) == 0 {
		_, err := os.Stat(current)
		return current, err == nil
	}

	seg := remaining[0]
	rest := remaining[1:]

	// Empty segment from "--" split means the next segment starts with ".".
	if seg == "" {
		if len(rest) == 0 {
			return "", false
		}
		dotSeg := "." + rest[0]
		rest = rest[1:]
		for _, sep := range []string{"/", ".", "-"} {
			candidate := current + sep + dotSeg
			if result, ok := DecodeSegments(candidate, rest); ok {
				return result, true
			}
		}
		return "", false
	}

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
