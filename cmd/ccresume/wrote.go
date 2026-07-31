package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tmc/cc"
)

// filesWrittenBy returns the set of file paths a session wrote to, extracted from
// its transcript entries. It reads the normalized cc model, so it works across
// harnesses: the structured write tools (Write/Edit/MultiEdit/NotebookEdit from
// Claude and opencode; write_to_file/replace_file_content from Antigravity) carry
// the path in a tool-input field, codex apply_patch carries it in the patch text,
// and shell writes (redirects, tee, sed -i, cp/mv, …) are parsed from Bash
// commands. Reads are never included. The result is deduplicated, order-stable.
func filesWrittenBy(entries []cc.Entry) []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, e := range entries {
		if e.Message == nil {
			continue
		}
		for _, tu := range e.Message.ToolUses() {
			for _, p := range writtenPathsForTool(tu) {
				add(p)
			}
		}
	}
	return out
}

// writeToolNames are the tool names, across harnesses, whose input names a file
// the session wrote. Claude and opencode normalize to Write/Edit/MultiEdit/
// NotebookEdit; Antigravity normalizes write_to_file/replace_file_content to
// Write but leaves multi_replace_file_content and the raw names unmapped, so both
// forms are listed. The path is read from whichever input key the harness uses.
var writeToolNames = map[string]bool{
	"Write":                      true,
	"Edit":                       true,
	"MultiEdit":                  true,
	"NotebookEdit":               true,
	"write_to_file":              true,
	"replace_file_content":       true,
	"multi_replace_file_content": true,
}

// writtenPathsForTool returns the file paths a single tool_use block wrote to.
func writtenPathsForTool(tu cc.ContentBlock) []string {
	// apply_patch (codex) carries paths inside a plain-text patch body, either as
	// its own tool or embedded in a shell heredoc; handle both by scanning any
	// input and the Bash command for the patch markers.
	if tu.Name == "apply_patch" {
		return applyPatchPaths(patchText(tu.Input))
	}
	if writeToolNames[tu.Name] {
		if p := firstInputPath(tu.Input); p != "" {
			return []string{p}
		}
		return nil
	}
	if cmd := tu.BashCommand(); cmd != "" {
		paths := bashWrittenFiles(cmd)
		// A shell heredoc apply_patch writes files too; catch its markers.
		paths = append(paths, applyPatchPaths(cmd)...)
		return paths
	}
	return nil
}

// firstInputPath returns the first non-empty file-path field in a tool input,
// checking every key harnesses use: file_path (Claude), filePath (opencode),
// notebook_path (Claude notebooks), TargetFile (Antigravity), and a bare path.
func firstInputPath(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return ""
	}
	for _, key := range []string{"file_path", "filePath", "notebook_path", "TargetFile", "path"} {
		if raw, ok := m[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

// patchText extracts the patch body from a codex apply_patch input. The reader
// wraps the raw patch string as {"input": "<patch>"}; fall back to treating the
// whole input as the patch when that shape is absent.
func patchText(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var wrapped struct {
		Input string `json:"input"`
	}
	if json.Unmarshal(input, &wrapped) == nil && wrapped.Input != "" {
		return wrapped.Input
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return s
	}
	return string(input)
}

// applyPatchMarker matches the file headers in an apply_patch body:
//
//	*** Add File: path
//	*** Update File: path
//	*** Delete File: path
var applyPatchMarker = regexp.MustCompile(`\*\*\* (?:Add|Update|Delete) File: (.+)`)

// applyPatchPaths returns the files named by apply_patch markers in text.
func applyPatchPaths(text string) []string {
	if !strings.Contains(text, "*** ") {
		return nil
	}
	var paths []string
	for _, m := range applyPatchMarker.FindAllStringSubmatch(text, -1) {
		if p := strings.TrimSpace(m[1]); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// Bash write patterns, mirroring cmd/ccfiles: the destinations a shell command
// creates or overwrites. Reads (cat, grep, <) are intentionally excluded.
var (
	bashRedirectRe = regexp.MustCompile(`(?:^|[^&0-9])>+\s+([a-zA-Z_~/.\-][^\s;|&]*)`)
	bashTeeRe      = regexp.MustCompile(`tee\s+(?:-a\s+)?(\S+)`)
	bashCpMvRe     = regexp.MustCompile(`(?:cp|mv)\s+(?:-[a-zA-Z]+\s+)*\S+\s+(\S+)`)
	bashHeredocRe  = regexp.MustCompile(`cat\s+>+\s*(\S+)\s*<<`)
	// sed -i, tolerating the BSD/macOS form with a separate backup-suffix arg and
	// any number of quoted script arguments before the file (sed -i '' 's/…/' f).
	bashSedRe   = regexp.MustCompile(`sed\s+-i['"e]?\s+(?:'[^']*'\s+)*(\S+)`)
	bashTouchRe = regexp.MustCompile(`touch\s+(\S+)`)
)

// bashWrittenFiles extracts written file paths from a shell command,
// deduplicated (a heredoc redirect, say, matches more than one pattern).
func bashWrittenFiles(cmd string) []string {
	if cmd == "" {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, re := range []*regexp.Regexp{bashRedirectRe, bashTeeRe, bashCpMvRe, bashHeredocRe, bashSedRe, bashTouchRe} {
		for _, m := range re.FindAllStringSubmatch(cmd, -1) {
			p := m[1]
			if p == "" || p == "-" || p == "." || strings.HasPrefix(p, "-") ||
				strings.HasPrefix(p, "/dev/") || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// pathMatches reports whether a written file path matches the -wrote pattern. A
// pattern with glob metacharacters (*?[) is matched with filepath.Match against
// both the full path and its basename, so "*.go" matches by basename and
// "cmd/ccresume/*.go" by the full path. Any other pattern is a case-insensitive
// substring, the same convenience the other content filters use.
func pathMatches(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	if strings.ContainsAny(pattern, "*?[") {
		if ok, _ := filepath.Match(pattern, path); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok {
			return true
		}
		return false
	}
	return strings.Contains(strings.ToLower(path), strings.ToLower(pattern))
}

// sessionWroteMatch reports whether a session wrote any file matching the
// pattern.
func sessionWroteMatch(path, pattern string) bool {
	entries, err := cc.ReadFile(context.Background(), path)
	if err != nil {
		return false
	}
	for _, f := range filesWrittenBy(entries) {
		if pathMatches(pattern, f) {
			return true
		}
	}
	return false
}

// filterWroteMatches keeps only sessions that wrote a file matching pattern.
func filterWroteMatches(matches []cc.IndexEntry, pattern string) []cc.IndexEntry {
	var out []cc.IndexEntry
	for _, m := range matches {
		if sessionWroteMatch(m.FullPath, pattern) {
			out = append(out, m)
		}
	}
	return out
}

// grepWroteMatches finds sessions modified within since that wrote a file
// matching pattern. It narrows candidates with a literal-substring ripgrep pass
// on the path text (which appears verbatim in the transcript — as an input
// value or a patch marker) before the exact structured check, so it does not
// read every recent session. A pattern with no usable literal falls back to
// scanning all recent sessions.
func grepWroteMatches(pattern string, since time.Duration) ([]cc.IndexEntry, error) {
	token := literalToken(pattern)
	var (
		matches []cc.IndexEntry
		err     error
	)
	if token != "" {
		matches, err = recentContentMatches(token, since)
	} else {
		matches, err = recentContentMatches("", since)
	}
	if err != nil {
		return nil, err
	}
	matches = filterWroteMatches(matches, pattern)
	cutoff := time.Now().Add(-since)
	out := matches[:0]
	for _, m := range matches {
		if m.ModifiedTime().After(cutoff) {
			out = append(out, m)
		}
	}
	return out, nil
}

// literalToken returns the longest run of non-glob characters in pattern, used
// to pre-filter candidate transcripts with a literal ripgrep search. For a plain
// substring pattern this is the whole pattern; for "*.go" it is ".go"; for
// "cmd/*/main.go" it is "/main.go".
func literalToken(pattern string) string {
	longest := ""
	for _, seg := range strings.FieldsFunc(pattern, func(r rune) bool {
		return r == '*' || r == '?' || r == '[' || r == ']'
	}) {
		if len(seg) > len(longest) {
			longest = seg
		}
	}
	return longest
}
