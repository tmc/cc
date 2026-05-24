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
	hasFlag    = flag.String("has", "", "Only include sessions whose transcript contains this literal text")
	cmdFlag    = flag.String("cmd", "", "Only include sessions with a tool command containing this literal text")
	resultFlag = flag.String("result", "", "Only include sessions with a tool result containing this literal text")
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
	has := strings.TrimSpace(*hasFlag)
	cmdText := strings.TrimSpace(*cmdFlag)
	resultText := strings.TrimSpace(*resultFlag)
	if query == "" && has != "" {
		query = has
	}
	if query == "" && cmdText != "" {
		query = cmdText
	}
	if query == "" && resultText != "" {
		query = resultText
	}
	if query == "" {
		return fmt.Errorf("no search query provided (use argument or clipboard)")
	}

	since, err := cc.ParseDuration(*sinceFlag)
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
		switch {
		case filepath.IsAbs(query):
			matches, err = recentContentMatches(jsonCWDField(query), since)
			if err != nil {
				return err
			}
		case query == has || query == cmdText || query == resultText:
			matches, err = recentContentMatches(query, since)
			if err != nil {
				return err
			}
		default:
			matches, err = grepMatches(query)
			if err != nil {
				return err
			}
		}
	}
	if has != "" {
		matches, err = filterContentMatches(matches, has)
		if err != nil {
			return err
		}
		if len(matches) == 0 && query == has {
			matches, err = grepContentMatches(has, since)
			if err != nil {
				return err
			}
		}
	}
	if cmdText != "" || resultText != "" {
		matches, err = filterToolMatches(matches, cmdText, resultText)
		if err != nil {
			return err
		}
		if len(matches) == 0 && (query == cmdText || query == resultText) {
			matches, err = grepToolMatches(query, cmdText, resultText, since)
			if err != nil {
				return err
			}
		}
	}

	if len(matches) == 0 {
		return fmt.Errorf("no sessions found matching %q", query)
	}

	curGitCommonDir := ""
	if cur, err := cc.ResolveGitContext(""); err == nil {
		curGitCommonDir = cur.GitCommonDir
	}

	if *oneFlag {
		sortIndexMatches(query, matches, curGitCommonDir)
		matches = matches[:1]
	}
	if *pathsFlag && !*launchFlag {
		sortIndexMatches(query, matches, curGitCommonDir)
		for _, m := range matches {
			fmt.Println(m.FullPath)
		}
		return nil
	}

	resolved := make([]resolvedMatch, len(matches))
	for i, m := range matches {
		resolved[i] = resolveMatch(query, m)
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		if resolved[i].occurrences != resolved[j].occurrences {
			return resolved[i].occurrences > resolved[j].occurrences
		}
		if curGitCommonDir != "" {
			ic := resolved[i].summary.GitCommonDir == curGitCommonDir
			jc := resolved[j].summary.GitCommonDir == curGitCommonDir
			if ic != jc {
				return ic
			}
		}
		return resolved[i].entry.ModifiedTime().After(resolved[j].entry.ModifiedTime())
	})

	for _, r := range resolved {
		bin, args := resumeInvocation(r.entry)
		if *launchFlag {
			return launchAgent(bin, args, r.target)
		}
		fmt.Println(renderResumeCommand(r.target, bin, args))
	}
	return nil
}

func sortIndexMatches(query string, matches []cc.IndexEntry, curGitCommonDir string) {
	sort.SliceStable(matches, func(i, j int) bool {
		if curGitCommonDir != "" {
			ic := matches[i].ProjectPath == curGitCommonDir
			jc := matches[j].ProjectPath == curGitCommonDir
			if ic != jc {
				return ic
			}
		}
		io := countIndexOccurrences(query, matches[i])
		jo := countIndexOccurrences(query, matches[j])
		if io != jo {
			return io > jo
		}
		return matches[i].ModifiedTime().After(matches[j].ModifiedTime())
	})
}

type resolvedMatch struct {
	entry       cc.IndexEntry
	summary     cc.SessionSummary
	target      string
	occurrences int
}

func resolveMatch(query string, m cc.IndexEntry) resolvedMatch {
	r := resolvedMatch{entry: m, target: m.ProjectPath}
	r.occurrences = countIndexOccurrences(query, m)
	entries, err := cc.ReadFile(m.FullPath)
	if err != nil {
		return r
	}
	r.summary = cc.Summarize(m.FullPath, entries)
	if n := countFileOccurrences(query, m.FullPath); n > r.occurrences {
		r.occurrences = n
	}
	if canPreferProjectPath(query, m) && dirExists(r.target) {
		r.target = preferredProjectPath(r.target)
		return r
	}
	for i := len(r.summary.DistinctCWDs) - 1; i >= 0; i-- {
		if dirExists(r.summary.DistinctCWDs[i]) {
			r.target = preferredProjectPath(r.summary.DistinctCWDs[i])
			return r
		}
	}
	if r.summary.GitCommonDir != "" && dirExists(r.summary.GitCommonDir) {
		r.target = preferredProjectPath(r.summary.GitCommonDir)
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
	r.target = preferredProjectPath(r.target)
	return r
}

func canPreferProjectPath(query string, m cc.IndexEntry) bool {
	if m.ProjectPath == "" {
		return false
	}
	if filepath.IsAbs(query) && m.ProjectPath == query {
		return true
	}
	return !strings.Contains(m.FullPath, string(filepath.Separator)+".codex"+string(filepath.Separator)+"sessions"+string(filepath.Separator))
}

func preferredProjectPath(path string) string {
	if path == "" {
		return ""
	}
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return path
		}
	}
	if home == "" {
		return path
	}
	src := filepath.Join(home, "go", "src")
	target, err := filepath.EvalSymlinks(src)
	if err != nil || target == src {
		return path
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		realPath = path
	}
	if realPath == target {
		return src
	}
	if strings.HasPrefix(realPath, target+string(os.PathSeparator)) {
		return src + realPath[len(target):]
	}
	return path
}

func countIndexOccurrences(query string, m cc.IndexEntry) int {
	return countOccurrences(query, m.SessionID) +
		countOccurrences(query, m.ProjectPath) +
		countOccurrences(query, m.FirstPrompt) +
		countOccurrences(query, m.Summary) +
		countOccurrences(query, m.GitBranch)
}

func countFileOccurrences(query, path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return countOccurrences(query, string(data))
}

func countOccurrences(query, text string) int {
	query = strings.ToLower(query)
	text = strings.ToLower(text)
	if query == "" || text == "" {
		return 0
	}
	n := 0
	for {
		i := strings.Index(text, query)
		if i < 0 {
			return n
		}
		n++
		text = text[i+len(query):]
	}
}

func jsonCWDField(path string) string {
	return `"cwd":"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

func filterContentMatches(matches []cc.IndexEntry, contains string) ([]cc.IndexEntry, error) {
	var filtered []cc.IndexEntry
	for _, m := range matches {
		ok, err := fileContains(m.FullPath, contains)
		if err != nil {
			return nil, err
		}
		if ok {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

func grepContentMatches(contains string, since time.Duration) ([]cc.IndexEntry, error) {
	matches, err := recentContentMatches(contains, since)
	if err != nil {
		return nil, err
	}
	matches, err = filterContentMatches(matches, contains)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-since)
	out := matches[:0]
	for _, m := range matches {
		if m.ModifiedTime().After(cutoff) {
			out = append(out, m)
		}
	}
	return out, nil
}

func fileContains(path, contains string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	return strings.Contains(string(data), contains), nil
}

func filterToolMatches(matches []cc.IndexEntry, cmdText, resultText string) ([]cc.IndexEntry, error) {
	var filtered []cc.IndexEntry
	for _, m := range matches {
		ok, err := fileHasToolMatch(m.FullPath, cmdText, resultText)
		if err != nil {
			return nil, err
		}
		if ok {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

func grepToolMatches(query, cmdText, resultText string, since time.Duration) ([]cc.IndexEntry, error) {
	matches, err := recentContentMatches(query, since)
	if err != nil {
		return nil, err
	}
	matches, err = filterToolMatches(matches, cmdText, resultText)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-since)
	out := matches[:0]
	for _, m := range matches {
		if m.ModifiedTime().After(cutoff) {
			out = append(out, m)
		}
	}
	return out, nil
}

func fileHasToolMatch(path, cmdText, resultText string) (bool, error) {
	if cmdText != "" && resultText == "" {
		return fileContains(path, cmdText)
	}
	if cmdText == "" && resultText != "" {
		return fileContains(path, resultText)
	}
	f, err := os.Open(path)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	cmdIDs := map[string]bool{}
	sawCommand := cmdText == ""
	sawResult := resultText == ""
	r := cc.NewReader(f)
	for r.Next() {
		e := r.Entry()
		if e.Message != nil {
			for _, tu := range e.Message.ToolUses() {
				if toolUseContains(tu, cmdText) {
					sawCommand = true
					if tu.ID != "" {
						cmdIDs[tu.ID] = true
					}
				}
			}
			for _, tr := range e.Message.ToolResults() {
				if resultText == "" || toolResultContains(tr, resultText) {
					if cmdText == "" || tr.ToolUseID == "" || cmdIDs[tr.ToolUseID] {
						sawResult = true
					}
				}
			}
		}
		if e.ToolUseResult != nil && (resultText == "" || toolUseResultContains(e.ToolUseResult, resultText)) {
			if cmdText == "" || e.UUID == "" || cmdIDs[e.UUID] {
				sawResult = true
			}
		}
		if sawCommand && sawResult {
			return true, nil
		}
	}
	if err := r.Err(); err != nil {
		return false, err
	}
	return sawCommand && sawResult, nil
}

func toolUseContains(tu cc.ContentBlock, text string) bool {
	if text == "" {
		return true
	}
	if strings.Contains(tu.BashCommand(), text) {
		return true
	}
	return strings.Contains(string(tu.Input), text)
}

func toolResultContains(tr cc.ContentBlock, text string) bool {
	if text == "" {
		return true
	}
	return strings.Contains(tr.Content, text) || strings.Contains(string(tr.Input), text)
}

func toolUseResultContains(res *cc.ToolUseResult, text string) bool {
	if text == "" {
		return true
	}
	return strings.Contains(res.Stdout, text) ||
		strings.Contains(res.Stderr, text) ||
		strings.Contains(res.Error, text) ||
		strings.Contains(string(res.Content), text)
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

type searchRoot struct {
	dir  string
	kind string
}

func sessionRoots() ([]searchRoot, error) {
	ch, err := cc.ClaudeHome()
	if err != nil {
		return nil, err
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
	return roots, nil
}

func recentContentMatches(query string, since time.Duration) ([]cc.IndexEntry, error) {
	roots, err := sessionRoots()
	if err != nil {
		return nil, err
	}
	minutes := int(since.Minutes())
	if minutes < 1 {
		minutes = 1
	}
	paths, err := recentMatchingPaths(roots, query, minutes)
	if err != nil {
		return nil, err
	}
	var matches []cc.IndexEntry
	cwdQuery, hasCWDQuery := parseJSONCWDField(query)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if hasCWDQuery {
			ok, err := fileHasSessionCWD(path, cwdQuery)
			if err != nil || !ok {
				continue
			}
		}
		var rootMatch searchRoot
		for _, root := range roots {
			if strings.HasPrefix(path, root.dir) {
				rootMatch = root
				break
			}
		}
		if rootMatch.dir == "" {
			continue
		}
		if e, ok := indexEntryForPath(path, rootMatch, info); ok {
			if hasCWDQuery {
				e.ProjectPath = cwdQuery
			} else if filepath.IsAbs(query) {
				e.ProjectPath = query
			}
			matches = append(matches, e)
		}
	}
	return matches, nil
}

func parseJSONCWDField(query string) (string, bool) {
	const prefix = `"cwd":"`
	if !strings.HasPrefix(query, prefix) || !strings.HasSuffix(query, `"`) {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(query, prefix), `"`), true
}

func fileHasSessionCWD(path, cwd string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	r := cc.NewReader(f)
	for r.Next() {
		e := r.Entry()
		if e.CWD == cwd && (e.Subtype == "session_meta" || e.Subtype == "turn_context" || e.Type == "user" || e.Type == "assistant") {
			return true, nil
		}
	}
	if err := r.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func recentMatchingPaths(roots []searchRoot, query string, minutes int) ([]string, error) {
	args := make([]string, 0, len(roots)+7)
	for _, root := range roots {
		args = append(args, root.dir)
	}
	args = append(args, "-type", "f", "-name", "*.jsonl", "-mmin", fmt.Sprintf("-%d", minutes), "-print")
	cmd := exec.Command("find", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("find: %w", err)
	}
	var paths []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		paths = append(paths, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	var matches []string
	const batchSize = 200
	for len(paths) > 0 {
		n := batchSize
		if len(paths) < n {
			n = len(paths)
		}
		batch := paths[:n]
		paths = paths[n:]
		args := append([]string{"-l", "-F", "--", query}, batch...)
		cmd := exec.Command("rg", args...)
		out, err := cmd.Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				continue
			}
			return nil, fmt.Errorf("rg: %w", err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			matches = append(matches, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	return matches, nil
}

// grepMatches searches file contents using rg
func grepMatches(query string) ([]cc.IndexEntry, error) {
	roots, err := sessionRoots()
	if err != nil {
		return nil, err
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

		var rootMatch searchRoot
		for _, root := range roots {
			if strings.HasPrefix(path, root.dir) {
				rootMatch = root
				break
			}
		}
		if rootMatch.dir == "" {
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if e, ok := indexEntryForPath(path, rootMatch, info); ok {
			matches = append(matches, e)
		}
	}
	return matches, nil
}

func indexEntryForPath(path string, root searchRoot, info os.FileInfo) (cc.IndexEntry, bool) {
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	projectPath := filepath.Dir(path)
	switch root.kind {
	case "codex":
		if strings.HasPrefix(sessionID, "rollout-") && len(sessionID) >= len("rollout-2006-01-02T15-04-05-")+1 {
			sessionID = strings.TrimPrefix(sessionID[len("rollout-2006-01-02T15-04-05-"):], "-")
		}
	default:
		rel, err := filepath.Rel(root.dir, path)
		if err != nil {
			return cc.IndexEntry{}, false
		}
		parts := strings.SplitN(rel, string(os.PathSeparator), 2)
		if len(parts) < 1 {
			return cc.IndexEntry{}, false
		}
		projectPath = decodePath(parts[0])
	}
	if !validSessionID(sessionID) {
		return cc.IndexEntry{}, false
	}
	return cc.IndexEntry{
		SessionID:   sessionID,
		FullPath:    path,
		ProjectPath: projectPath,
		Modified:    info.ModTime().Format(time.RFC3339Nano),
	}, true
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
// as '\”.
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
