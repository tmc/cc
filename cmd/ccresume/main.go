package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

var (
	launchFlag = flag.Bool("l", false, "Launch claude instead of printing command")
	sinceFlag  = flag.String("since", "7d", "Search sessions modified within duration")
	oneFlag    = flag.Bool("1", false, "Show only the most recent match")
	clipFlag   = flag.Bool("clip", true, "Use clipboard as search query (pbpaste)")
	pathsFlag  = flag.Bool("paths", false, "Print raw session file paths instead of resume commands")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccresume: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	query := strings.Join(flag.Args(), " ")
	if query == "" && *clipFlag {
		out, err := exec.Command("pbpaste").Output()
		if err == nil {
			query = strings.TrimSpace(string(out))
		}
	}
	if query == "" {
		return fmt.Errorf("no search query provided (use argument or clipboard)")
	}

	since, err := parseDuration(*sinceFlag)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}

	// Try index first
	matches, err := findMatches(query, since)
	if err != nil {
		return err
	}

	// Fall back to content search if no matches in index
	if len(matches) == 0 {
		matches, err = grepMatches(query)
		if err != nil {
			return err
		}
	}

	if len(matches) == 0 {
		return fmt.Errorf("no sessions found matching %q", query)
	}

	resolved := make([]resolvedMatch, len(matches))
	for i, m := range matches {
		resolved[i] = resolveMatch(m)
	}

	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].entry.ModifiedTime().After(resolved[j].entry.ModifiedTime())
	})

	if cur, err := cc.ResolveGitContext(""); err == nil && cur.GitCommonDir != "" {
		for i, r := range resolved {
			if r.summary.GitCommonDir == cur.GitCommonDir {
				resolved = append([]resolvedMatch{r}, append(resolved[:i], resolved[i+1:]...)...)
				break
			}
		}
	}

	if *oneFlag {
		resolved = resolved[:1]
	}

	for _, r := range resolved {
		bin, args := resumeInvocation(r.entry)
		if *launchFlag {
			return launchAgent(bin, args, r.target)
		}
		if *pathsFlag {
			fmt.Println(r.entry.FullPath)
			continue
		}
		fmt.Println(renderResumeCommand(r.target, bin, args))
	}
	return nil
}

type resolvedMatch struct {
	entry   cc.IndexEntry
	summary cc.SessionSummary
	target  string
}

func resolveMatch(m cc.IndexEntry) resolvedMatch {
	r := resolvedMatch{entry: m, target: m.ProjectPath}
	entries, err := cc.ReadFile(m.FullPath)
	if err != nil {
		return r
	}
	r.summary = cc.Summarize(m.FullPath, entries)
	for i := len(r.summary.DistinctCWDs) - 1; i >= 0; i-- {
		if dirExists(r.summary.DistinctCWDs[i]) {
			r.target = r.summary.DistinctCWDs[i]
			return r
		}
	}
	if r.summary.GitCommonDir != "" && dirExists(r.summary.GitCommonDir) {
		r.target = r.summary.GitCommonDir
		return r
	}
	// Nothing recorded still exists on disk. The default ProjectPath comes
	// from naively decoding the encoded session directory name, where '-'
	// is ambiguous (literal '-' vs path separator). If that decoded path
	// also doesn't exist, prefer the session's recorded cwd verbatim — at
	// least it matches what was actually written.
	if !dirExists(r.target) {
		if r.summary.CWD != "" {
			r.target = r.summary.CWD
		} else if len(r.summary.DistinctCWDs) > 0 {
			r.target = r.summary.DistinctCWDs[len(r.summary.DistinctCWDs)-1]
		}
	}
	return r
}

func findMatches(query string, since time.Duration) ([]cc.IndexEntry, error) {
	entries, err := cc.AllIndexEntries(since, "")
	if err != nil {
		return nil, err
	}

	var matches []cc.IndexEntry
	q := strings.ToLower(query)
	for _, e := range entries {
		if !validSessionID(e.SessionID) {
			continue
		}
		if containsAny(q, e.SessionID, e.ProjectPath, e.FirstPrompt, e.Summary, e.GitBranch) {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

// grepMatches searches file contents using rg
func grepMatches(query string) ([]cc.IndexEntry, error) {
	ch, err := cc.ClaudeHome()
	if err != nil {
		return nil, err
	}
	type searchRoot struct {
		dir  string
		kind string
	}
	roots := []searchRoot{{dir: filepath.Join(ch, "projects"), kind: "claude"}}

	gh, _ := cc.GeminiHome()
	if gh != "" {
		if d := filepath.Join(gh, "projects"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "gemini"})
		}
	}
	xh, _ := cc.CodexHome()
	if xh != "" {
		if d := filepath.Join(xh, "sessions"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "codex"})
		}
	}

	args := []string{"-l", query}
	for _, root := range roots {
		args = append(args, root.dir)
	}
	cmd := exec.Command("rg", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("rg: %w", err)
	}

	var matches []cc.IndexEntry
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		path := scanner.Text()
		if !strings.HasSuffix(path, ".jsonl") {
			continue
		}

		var (
			rootDir  string
			rootKind string
		)
		for _, root := range roots {
			if strings.HasPrefix(path, root.dir) {
				rootDir = root.dir
				rootKind = root.kind
				break
			}
		}
		if rootDir == "" {
			continue
		}

		sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		projectPath := filepath.Dir(path)
		switch rootKind {
		case "codex":
			entries, err := cc.ReadFile(path)
			if err == nil {
				sum := cc.Summarize(path, entries)
				if sum.SessionID != "" {
					sessionID = sum.SessionID
				}
				if sum.CWD != "" {
					projectPath = sum.CWD
				}
			}
		default:
			rel, err := filepath.Rel(rootDir, path)
			if err != nil {
				continue
			}
			parts := strings.SplitN(rel, string(os.PathSeparator), 2)
			if len(parts) < 1 {
				continue
			}
			projectPath = decodePath(parts[0])
		}
		if !validSessionID(sessionID) {
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		matches = append(matches, cc.IndexEntry{
			SessionID:   sessionID,
			FullPath:    path,
			ProjectPath: projectPath,
			Modified:    info.ModTime().Format(time.RFC3339Nano),
		})
	}
	return matches, nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// decodePath reconstructs the original filesystem path from an encoded
// Claude Code project directory name.
func decodePath(encoded string) string {
	if encoded == "" || !strings.HasPrefix(encoded, "-") {
		return encoded
	}
	segments := strings.Split(encoded[1:], "-")
	if len(segments) == 0 {
		return "/"
	}
	if result, ok := cc.DecodeSegments("/"+segments[0], segments[1:]); ok {
		return result
	}
	return "/" + strings.Join(segments, "/")
}

// validSessionID reports whether a session ID is a valid resume target.
// Compacted sessions (agent-acompact-*) are internal and cannot be resumed.
func validSessionID(id string) bool {
	return !strings.HasPrefix(id, "agent-acompact-")
}

func containsAny(query string, fields ...string) bool {
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), query) {
			return true
		}
	}
	return false
}

func resumeInvocation(m cc.IndexEntry) (string, []string) {
	switch {
	case strings.Contains(m.FullPath, string(filepath.Separator)+".codex"+string(filepath.Separator)):
		if m.SessionID != "" {
			return "codex", []string{"resume", m.SessionID}
		}
		return "codex", []string{"resume"}
	case strings.Contains(m.FullPath, string(filepath.Separator)+".gemini"+string(filepath.Separator)):
		if m.SessionID != "" {
			return "gemini", []string{"-r", m.SessionID}
		}
		return "gemini", nil
	default:
		if m.SessionID != "" {
			return "claude", []string{"-r", m.SessionID}
		}
		return "claude", nil
	}
}

func renderResumeCommand(projectPath, bin string, args []string) string {
	parts := append([]string{bin}, args...)
	for i, p := range parts {
		parts[i] = shellQuote(p)
	}
	cmd := strings.TrimSpace(strings.Join(parts, " "))
	if projectPath == "" {
		return cmd
	}
	return fmt.Sprintf("cd %s; %s", shellQuote(projectPath), cmd)
}

// shellQuote wraps s in single quotes for POSIX shells when it contains any
// character that the shell would interpret. Single quotes inside s are escaped
// as '\''.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '_', c == '-', c == '.', c == '/', c == ':', c == '@', c == '+', c == ',', c == '=':
			// safe shell-bareword character
		default:
			safe = false
		}
		if !safe {
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func launchAgent(bin string, args []string, projectPath string) error {
	if projectPath != "" {
		if err := os.Chdir(projectPath); err != nil {
			return fmt.Errorf("chdir %s: %w", projectPath, err)
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	suffix := s[len(s)-1]
	numStr := s[:len(s)-1]
	var num int
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
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
