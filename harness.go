package cc

import (
	"path/filepath"
	"strings"

	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/antigravityread"
	"github.com/tmc/cc/internal/geminiread"
	"github.com/tmc/cc/internal/opencoderead"
	"github.com/tmc/cc/internal/piread"
)

// Harness reports which coding-agent harness produced the session at path,
// one of "claude", "codex", "gemini", "opencode", "pi", "antigravity", or
// "unknown" when path is under no known harness home. It classifies by path
// only and performs no more I/O than the IsSessionPath predicates it reuses;
// it does not read or decode the session. Entry.Source is deliberately not
// consulted: the claude and codex readers do not set it to a harness name.
func Harness(path string) string {
	// 1. Predicate tier — same order as ReadFile, so classification agrees
	//    with which reader actually parses the file.
	if opencoderead.IsSessionPath(path) {
		return "opencode"
	}
	if piread.IsSessionPath(path) {
		return "pi"
	}
	if antigravityread.IsSessionPath(path) {
		return "antigravity"
	}
	if geminiread.IsSessionPath(path) {
		return "gemini"
	}
	// 2. Home-prefix tier — claude, codex, gemini all fall through to
	//    ReadAll/codexread and are indistinguishable by predicate, so use the
	//    resolved home roots. Resolve once; ignore resolver errors (a missing
	//    home yields "" which under() never matches).
	abs := path
	if a, err := filepath.Abs(path); err == nil {
		abs = a
	}
	if ah, _ := ccpaths.AntigravityHome(); under(abs, ah) {
		return "antigravity" // ANTIGRAVITY_HOME default ~/.gemini/antigravity:
	} //                        must be tested before gemini (subset of ~/.gemini)
	if gh, _ := ccpaths.GeminiHome(); under(abs, filepath.Join(gh, "antigravity-cli")) {
		return "antigravity" // ~/.gemini/antigravity-cli/brain
	}
	if xh, _ := ccpaths.CodexHome(); under(abs, filepath.Join(xh, "sessions")) {
		return "codex"
	}
	if ch, _ := ccpaths.ClaudeHome(); under(abs, filepath.Join(ch, "projects")) {
		return "claude"
	}
	if gh, _ := ccpaths.GeminiHome(); under(abs, filepath.Join(gh, "projects")) || under(abs, filepath.Join(gh, "tmp")) {
		return "gemini"
	}
	return "unknown"
}

// under reports whether path is dir or lies beneath it. dir=="" never matches.
func under(path, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
