package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass/it2bind"
	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/cchooks"
	"github.com/tmc/cc/ccpaths"
)

var (
	launchFlag   = flag.Bool("l", false, "Launch claude instead of printing command")
	sinceFlag    = flag.String("since", "7d", "Search sessions modified within duration")
	oneFlag      = flag.Bool("1", false, "Show only the most recent match")
	clipFlag     = flag.Bool("clip", true, "Use clipboard as search query (pbpaste)")
	pathsFlag    = flag.Bool("paths", false, "Print raw session file paths instead of resume commands")
	hasFlag      = flag.String("has", "", "Only include sessions whose transcript contains this literal text")
	cmdFlag      = flag.String("cmd", "", "Only include sessions with a tool command containing this literal text")
	resultFlag   = flag.String("result", "", "Only include sessions with a tool result containing this literal text")
	wroteFlag    = flag.String("wrote", "", "Only include sessions that wrote a file matching this path or glob")
	dirFlag      = flag.String("dir", "", "Only include sessions recorded in this directory")
	harnessFlag  = flag.String("harness", "", "Only include sessions from this harness")
	sidSideFlag  = flag.String("sid-side", "src", "When a query is an it2 send-text line, resume the src or dst pane")
	sortFlag     = flag.String("sort", "relevance", "Order matches by relevance or recency")
	infoFlag     = flag.Bool("info", false, "Annotate each resume line with session name, size, an occurrence sparkline, and running state (implies -show-is-running)")
	runningFlag  = flag.Bool("show-is-running", false, "Annotate each resume line whose session is already running as a process")
	infoLineFlag = flag.Bool("info-line", false, "Print the -info annotation on its own line under each resume command (implies -info)")
	noColorFlag  = flag.Bool("no-color", false, "Disable ANSI color in -info annotations")
)

const (
	ansiDim       = "\033[2m"
	ansiReverse   = "\033[7m"
	ansiGreen     = "\033[32m"
	ansiBoldGreen = "\033[1;32m"
	ansiCyan      = "\033[36m"
	ansiBoldCyan  = "\033[1;36m"
	ansiReset     = "\033[0m"
)

// annotateColCap bounds the column the -info annotations are aligned to:
// commands wider than this don't widen the column, they just overrun it.
const annotateColCap = 100

// useColor reports whether ANSI styling should be written to f: only when f is a
// terminal, NO_COLOR is unset, and -no-color was not passed. This mirrors the
// convention in cmd/cass.
func useColor(f *os.File) bool {
	if *noColorFlag || os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func main() {
	flag.Parse()
	// -info-line is a layout variant of -info, and -info is the "tell me about
	// each match" mode, where whether the session is already open belongs in
	// the picture; the process scan behind it is a single ps snapshot, cheap
	// next to the transcript reads -info does anyway.
	if *infoLineFlag {
		*infoFlag = true
	}
	if *infoFlag {
		*runningFlag = true
	}
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccresume: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dir, err := cleanDirectory(*dirFlag)
	if err != nil {
		return err
	}
	harness, err := cleanHarness(*harnessFlag)
	if err != nil {
		return err
	}
	query := strings.Join(flag.Args(), " ")
	has := strings.TrimSpace(*hasFlag)
	cmdText := strings.TrimSpace(*cmdFlag)
	resultText := strings.TrimSpace(*resultFlag)
	wrote := strings.TrimSpace(*wroteFlag)
	// The clipboard fallback only applies when nothing else names what to search.
	// An explicit content filter (-has/-cmd/-result/-wrote) is the intended query
	// and must not be overridden by whatever happens to be on the clipboard.
	if query == "" && *clipFlag && has == "" && cmdText == "" && resultText == "" && wrote == "" {
		out, err := exec.Command("pbpaste").Output()
		if err == nil {
			query = strings.TrimSpace(string(out))
		}
	}
	if query == "" && has != "" {
		query = has
	}
	if query == "" && cmdText != "" {
		query = cmdText
	}
	if query == "" && resultText != "" {
		query = resultText
	}
	if query == "" && wrote != "" {
		query = wrote
	}
	if query == "" && dir != "" {
		query = dir
	}
	if query == "" {
		return fmt.Errorf("no search query provided (use argument or clipboard)")
	}
	// A claude.ai artifact/session/design URL (e.g. copied from the web app,
	// query string and all) is reduced to the bare identifier so it matches the
	// creating session's transcript. The artifact UUID appears verbatim in the
	// session that published it, so the content search resolves the resume
	// command for that session.
	// A URL or it2 marker resolves to a specific session id rather than being a
	// content search. When that happens, the -info sparkline (occurrences over
	// the session's timeline) is meaningless, so remember whether the query was
	// rewritten by the resolution pipeline below.
	rawQuery := query
	query = normalizeClaudeURL(query)
	// A copied it2 log line ([it2:send-text src=X dst=Y]) or collab-msg header
	// (<collab-msg src="X">) is reduced to the bare pane ID for the chosen side,
	// so pasting one of those lines resolves the pane's session too.
	query = extractIt2SID(query, *sidSideFlag)
	// An iTerm2 session ID (a pane UUID, optionally with the wNtNpN: prefix) is
	// resolved to the harness session most recently bound to that pane, so
	// pasting a pane ID resumes the session that ran there.
	query = resolveIt2SID(query)
	isIDResolution := query != rawQuery

	since, err := ccpaths.ParseDuration(*sinceFlag)
	if err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	if *sortFlag != "relevance" && *sortFlag != "recency" {
		return fmt.Errorf("invalid -sort %q (want relevance or recency)", *sortFlag)
	}

	search := searchSpec{
		query:      query,
		has:        has,
		cmdText:    cmdText,
		resultText: resultText,
		wrote:      wrote,
		dir:        dir,
		harness:    harness,
	}
	matches, err := search.run(since)
	if err != nil {
		return err
	}
	// The -since default is ccresume's own speed bound, not part of the query:
	// selecting candidates by mtime keeps the common search fast, but it also
	// hides older sessions that a plain grep over the transcripts would find.
	// When the bounded search comes up empty and nobody asked for the bound,
	// search all history rather than report that nothing matches. An explicit
	// -since is the user's own bound and is left alone.
	if len(matches) == 0 && !sinceSet() {
		fmt.Fprintf(os.Stderr, "ccresume: nothing modified within %s matches %q; searching all history\n", *sinceFlag, query)
		matches, err = search.run(allHistory)
		if err != nil {
			return err
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("no sessions found matching %q", query)
	}

	curGitCommonDir := ""
	if cur, err := ccgit.ResolveGitContext(""); err == nil {
		curGitCommonDir = cur.GitCommonDir
	}

	// -running scans the process table once so every line is annotated against a
	// single consistent snapshot; a nil slice (ps failed or flag unset) simply
	// annotates nothing.
	var procs []runningProc
	if *runningFlag {
		procs = scanProcesses()
	}

	if *oneFlag {
		sortIndexMatches(query, matches, curGitCommonDir)
		matches = matches[:1]
	}
	if *pathsFlag && !*launchFlag {
		sortIndexMatches(query, matches, curGitCommonDir)
		for _, m := range matches {
			fmt.Println(rawSessionPath(m.FullPath))
			if *infoFlag || *runningFlag {
				// Paths stay bare on stdout; the annotation goes to stderr.
				r := resolveMatch(query, m)
				bin, args := resumeInvocation(m)
				if ann := annotationStderr(query, r, isIDResolution, procs, bin, args); ann != "" {
					fmt.Fprintln(os.Stderr, dim(ann, useColor(os.Stderr)))
				}
			}
		}
		return nil
	}

	// Resolving a match decodes its whole transcript (cc.SummarizeFile), which
	// for large sessions dominates the run time. Resolve concurrently, bounded
	// so a wide match set doesn't saturate memory.
	resolved := make([]resolvedMatch, len(matches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, m := range matches {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resolved[i] = resolveMatch(query, m)
		}()
	}
	wg.Wait()
	sort.SliceStable(resolved, func(i, j int) bool {
		if *sortFlag == "recency" {
			return resolved[i].entry.ModifiedTime().After(resolved[j].entry.ModifiedTime())
		}
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

	// Render every command first so the -info annotations can be aligned into
	// one column: pad each command to the widest one, capped so a single
	// outlier path doesn't push the whole column off-screen. Lines past the
	// cap just run long.
	lines := make([]string, len(resolved))
	runs := make([]string, len(resolved))
	annotateCol := 0
	for i, r := range resolved {
		bin, args := resumeInvocation(r.entry)
		if *launchFlag {
			if *infoFlag || *runningFlag {
				// The command is exec'd directly, so keep the launch clean and
				// log the annotation to stderr instead of appending a comment.
				if ann := annotationStderr(query, r, isIDResolution, procs, bin, args); ann != "" {
					fmt.Fprintln(os.Stderr, dim(ann, useColor(os.Stderr)))
				}
			}
			return launchAgent(bin, args, r.target)
		}
		lines[i] = renderResumeCommand(r.target, bin, args)
		if w := utf8.RuneCountInString(lines[i]); w > annotateCol && w <= annotateColCap {
			annotateCol = w
		}
		if *runningFlag {
			runs[i] = runningMarker(procs, bin, args)
		}
	}
	// A trailing shell comment keeps each line pipe/copy-safe: sh ignores
	// everything after '#'. The comment is reference, not payload, so it is
	// dimmed on a terminal; piped output stays byte-clean.
	on := useColor(os.Stdout)
	anns := alignedAnnotations(query, resolved, runs, isIDResolution, on)
	for i := range resolved {
		line := lines[i]
		if !*infoLineFlag && anns[i] != "" {
			if pad := annotateCol - utf8.RuneCountInString(line); pad > 0 {
				line += strings.Repeat(" ", pad)
			}
		}
		fmt.Println(attachAnnotation(line, anns[i], on))
	}
	return nil
}

// rawSessionPath returns the file that contains the complete transcript for a
// session. Antigravity stores sessions as directories and keeps a compact
// transcript beside the full-fidelity copy; callers of -paths need the latter.
func rawSessionPath(path string) string {
	if !isAntigravityResumePath(path) {
		return path
	}
	full := filepath.Join(path, ".system_generated", "logs", "transcript_full.jsonl")
	if _, err := os.Stat(full); err == nil {
		return full
	}
	compact := filepath.Join(path, ".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(compact); err == nil {
		return compact
	}
	return path
}

func cleanDirectory(dir string) (string, error) {
	if dir == "" {
		return "", nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	return filepath.Clean(abs), nil
}

func cleanHarness(harness string) (string, error) {
	harness = strings.ToLower(strings.TrimSpace(harness))
	switch harness {
	case "", "claude", "codex", "gemini", "opencode", "pi", "antigravity":
		return harness, nil
	default:
		return "", fmt.Errorf("unknown harness %q", harness)
	}
}

func filterMatches(matches []cc.IndexEntry, dir, harness string) []cc.IndexEntry {
	out := matches[:0]
	for _, m := range matches {
		if harness != "" && cc.Harness(m.FullPath) != harness {
			continue
		}
		if dir != "" && !matchDirectory(m, dir) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func matchDirectory(m cc.IndexEntry, dir string) bool {
	if sameDirectory(m.ProjectPath, dir) {
		return true
	}
	entries, err := cc.ReadFile(context.Background(), m.FullPath)
	if err != nil {
		return false
	}
	s := cc.Summarize(m.FullPath, entries)
	if sameDirectory(s.CWD, dir) {
		return true
	}
	for _, cwd := range s.DistinctCWDs {
		if sameDirectory(cwd, dir) {
			return true
		}
	}
	return false
}

func sameDirectory(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	return filepath.Clean(aa) == filepath.Clean(b)
}

func sortIndexMatches(query string, matches []cc.IndexEntry, curGitCommonDir string) {
	if *sortFlag == "recency" {
		sort.SliceStable(matches, func(i, j int) bool {
			return matches[i].ModifiedTime().After(matches[j].ModifiedTime())
		})
		return
	}
	// Count occurrences once per match: transcript content when the query is
	// only found there (e.g. an artifact URL, which appears in the creating
	// session's tool results but in no index field), otherwise the cheaper
	// index-metadata count. This keeps the -1/--paths ordering consistent with
	// the default sort, which ranks by occurrences before the cwd tiebreaker.
	counts := make([]int, len(matches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, m := range matches {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			n := countIndexOccurrences(query, m)
			if n == 0 {
				n = countFileOccurrences(query, m.FullPath)
			}
			counts[i] = n
		}()
	}
	wg.Wait()
	occ := make(map[string]int, len(matches))
	for i, m := range matches {
		occ[m.FullPath] = counts[i]
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if oi, oj := occ[matches[i].FullPath], occ[matches[j].FullPath]; oi != oj {
			return oi > oj
		}
		if curGitCommonDir != "" {
			ic := matches[i].ProjectPath == curGitCommonDir
			jc := matches[j].ProjectPath == curGitCommonDir
			if ic != jc {
				return ic
			}
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
	summary, err := cc.SummarizeFileCached(context.Background(), m.FullPath)
	if err != nil {
		return r
	}
	if summary.CWD != "" {
		if git, err := ccgit.ResolveGitContext(summary.CWD); err == nil {
			summary.GitContext = git
		}
	}
	r.summary = summary
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
	// A target inside session storage (an antigravity brain conversation dir)
	// is worse than none: agy treats the resume cwd as the workspace, so
	// cd'ing into storage would point the resumed agent at its own transcript.
	if isAgentStoragePath(r.target) {
		r.target = ""
		return r
	}
	r.target = preferredProjectPath(r.target)
	return r
}

// isAgentStoragePath reports whether path lies inside a harness's session
// storage rather than a real working directory.
func isAgentStoragePath(path string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(path, sep+"antigravity"+sep+"brain"+sep) ||
		strings.Contains(path, sep+"antigravity-cli"+sep+"brain"+sep) ||
		strings.Contains(path, sep+".codex"+sep+"sessions"+sep)
}

func canPreferProjectPath(query string, m cc.IndexEntry) bool {
	if m.ProjectPath == "" {
		return false
	}
	if filepath.IsAbs(query) && m.ProjectPath == query {
		return true
	}
	return !isAgentStoragePath(m.FullPath)
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

// countFileOccurrences counts case-insensitive occurrences of query in the file
// at path. Transcripts reach hundreds of megabytes, so the count comes from rg,
// which ccresume already relies on for the content search; reading the file
// into memory is the fallback.
func countFileOccurrences(query, path string) int {
	if n, ok := rgCountMatches(query, path); ok {
		return n
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return countOccurrences(query, string(data))
}

// rgCountMatches counts the non-overlapping case-insensitive matches of the
// literal query in path, reporting whether rg produced an answer.
func rgCountMatches(query, path string) (int, bool) {
	if query == "" {
		return 0, true
	}
	out, err := exec.Command("rg", "--count-matches", "--no-filename", "-a", "-F", "-i", "--", query, path).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, true // no match
		}
		return 0, false
	}
	n := 0
	for _, line := range splitLines(out) {
		c, err := strconv.Atoi(line)
		if err != nil {
			return 0, false
		}
		n += c
	}
	return n, true
}

// annotateLine appends the enabled annotations to a rendered resume command as
// trailing shell comments: first the -info size/occurrence detail, then — at the
// very end of the line — a "# RUNNING" comment for any match whose session is
// already open in a running process. Both are comments, so the line stays safe
// to pipe into sh (everything after the first '#' is ignored). on selects ANSI
// dimming. bin and args are the resume invocation, used both to match the session
// against running processes and, via resumeToken, as the identifier to look for.
func annotateLine(line, query string, r resolvedMatch, isIDResolution bool, procs []runningProc, bin string, args []string, on bool) string {
	var runs []string
	if *runningFlag {
		runs = []string{runningMarker(procs, bin, args)}
	} else {
		runs = []string{""}
	}
	return attachAnnotation(line, alignedAnnotations(query, []resolvedMatch{r}, runs, isIDResolution, on)[0], on)
}

// attachAnnotation joins a rendered command and its annotation into the output
// layout: a trailing "# ..." comment, or with -info-line an indented comment
// line below the command. Both layouts stay sh-safe since the annotation is a
// comment either way. An empty annotation leaves the line untouched.
func attachAnnotation(line, ann string, on bool) string {
	if ann == "" {
		return line
	}
	ann = dim("# "+ann, on)
	if *infoLineFlag {
		return line + "\n  " + ann
	}
	return line + "   " + ann
}

// annRow holds one match's plain (unstyled, unpadded) annotation fields.
type annRow struct {
	msgs, lines, hits, dense, spark, age, run, name string
	sparkCounts                                     []int
	recent                                          bool
}

// annotationRows extracts each match's annotation fields. Fields that don't
// apply (no line count, no content occurrences) are left empty; runs carries
// the per-row RUNNING marker, computed by the caller, which knows the resume
// invocation each row was matched against.
func annotationRows(query string, resolved []resolvedMatch, runs []string, isIDResolution bool) []annRow {
	rows := make([]annRow, len(resolved))
	for i, r := range resolved {
		row := &rows[i]
		// MessageCount is free from the index, but the grep fallback builds
		// entries without it; fall back to the summary, which is read anyway.
		msgs := r.entry.MessageCount
		if msgs == 0 {
			msgs = r.summary.UserMessages + r.summary.AsstMessages
		}
		row.msgs = humanize(msgs) + "×"
		if r.summary.TotalLines > 0 {
			row.lines = humanize(r.summary.TotalLines) + "L"
		}
		// An id-resolution (a claude.ai URL or it2 SID that resolved to a
		// specific session) has no meaningful occurrences-over-time, so it
		// shows size and time only.
		if !isIDResolution && r.occurrences > 0 {
			counts, densePct := occurrenceBuckets(query, r.entry.FullPath, 12)
			row.hits = humanize(r.occurrences) + "@"
			row.dense = strconv.Itoa(densePct) + "%"
			row.spark = renderSpark(counts)
			row.sparkCounts = counts
		}
		row.age = relAge(r.entry.ModifiedTime())
		if t := r.entry.ModifiedTime(); !t.IsZero() && time.Since(t) < time.Hour {
			row.recent = true
		}
		row.run = runs[i]
		row.name = r.summary.CustomTitle
	}
	return rows
}

// alignedAnnotations renders each match's annotation with every field padded
// to a shared column width across the whole result set, so the annotations
// read as a table down the listing: counts and age right-align, the sparkline
// and RUNNING marker left-align, and the variable-width session name comes
// last so it never shifts another column. Styling (dim scaffolding, cyan
// density+sparkline, green fresh age, bold-green RUNNING, reverse-video name)
// is applied after padding so widths are computed over plain text.
func alignedAnnotations(query string, resolved []resolvedMatch, runs []string, isIDResolution, on bool) []string {
	rows := annotationRows(query, resolved, runs, isIDResolution)
	var w annRow // column widths, stored as pad targets via maxw
	for i := range rows {
		r := &rows[i]
		// The ● glyph rides only on styled output so piped text keeps the
		// bare RUNNING token; it participates in the column width.
		if on && r.run != "" {
			r.run = "● " + r.run
		}
		w.msgs = maxw(w.msgs, r.msgs)
		w.lines = maxw(w.lines, r.lines)
		w.hits = maxw(w.hits, r.hits)
		w.dense = maxw(w.dense, r.dense)
		w.spark = maxw(w.spark, r.spark)
		w.age = maxw(w.age, r.age)
		w.run = maxw(w.run, r.run)
		w.name = maxw(w.name, r.name)
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		// Cells are collected as plain/styled pairs: widths and the
		// trailing-blank trim work on plain text, the output joins styled.
		var plain, styled []string
		emit := func(p, s string) { plain = append(plain, p); styled = append(styled, s) }
		if *infoFlag {
			p := padLeft(r.msgs, utf8.RuneCountInString(w.msgs))
			emit(p, p)
			if w.lines != "" {
				p := padLeft(r.lines, utf8.RuneCountInString(w.lines))
				emit(p, p)
			}
			if w.hits != "" {
				p := padLeft(r.hits, utf8.RuneCountInString(w.hits))
				emit(p, p)
			}
			if w.dense != "" {
				// Density and sparkline form one cyan cell: together they are
				// the shape of relevance — central vs incidental, and when.
				// Styled sparklines grade each bucket by intensity.
				p := padLeft(r.dense, utf8.RuneCountInString(w.dense)) + " " + padRight(r.spark, utf8.RuneCountInString(w.spark))
				s := p
				if r.spark != "" && on {
					s = hl(padLeft(r.dense, utf8.RuneCountInString(w.dense)), ansiCyan, on) + " " +
						renderSparkStyled(r.sparkCounts) + strings.Repeat(" ", utf8.RuneCountInString(w.spark)-utf8.RuneCountInString(r.spark))
				}
				emit(p, s)
			}
			// A fresh session is likely the one being hunted for; brighten it.
			p = padLeft(r.age, utf8.RuneCountInString(w.age))
			s := p
			if r.recent {
				s = hl(p, ansiGreen, on)
			}
			emit(p, s)
		}
		// The user-assigned name sits between the stats and the RUNNING
		// marker: reverse-video the way Claude Code's own UI shows it, quoted
		// in plain output. Both renderings are 2 runes wider than the bare
		// name (padding spaces / quotes), so the cell width is w.name+2, and
		// the cell is padded (or emitted blank) only when a RUNNING marker
		// follows on this row, keeping that column aligned without ever
		// ending a line in blanks.
		if w.name != "" && (r.name != "" || r.run != "") {
			cell, sCell := "", ""
			if r.name != "" {
				if on {
					cell, sCell = " "+r.name+" ", hl(" "+r.name+" ", ansiReverse, on)
				} else {
					cell = strconv.Quote(r.name)
					sCell = cell
				}
			}
			if r.run != "" {
				pad := utf8.RuneCountInString(w.name) + 2 - utf8.RuneCountInString(cell)
				if pad > 0 {
					cell += strings.Repeat(" ", pad)
					sCell += strings.Repeat(" ", pad)
				}
			}
			emit(cell, sCell)
		}
		if *runningFlag && r.run != "" {
			// Bold green, on the right edge: the one glanceable "this
			// session is live" signal.
			emit(r.run, hl(r.run, ansiBoldGreen, on))
		}
		for len(plain) > 0 && strings.TrimSpace(plain[len(plain)-1]) == "" {
			plain = plain[:len(plain)-1]
			styled = styled[:len(styled)-1]
		}
		out[i] = strings.Join(styled, " ")
	}
	return out
}

// maxw returns whichever of the running-widest example a and candidate s is
// wider, measured in runes.
func maxw(a, s string) string {
	if utf8.RuneCountInString(s) > utf8.RuneCountInString(a) {
		return s
	}
	return a
}

// padLeft right-aligns s in a w-rune cell; padRight left-aligns it.
func padLeft(s string, w int) string {
	if n := w - utf8.RuneCountInString(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

func padRight(s string, w int) string {
	if n := w - utf8.RuneCountInString(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// annotationStderr builds the annotation text logged to stderr in -paths and -l
// modes, where it cannot ride on the command line. It mirrors annotateLine's
// ordering (info detail, then RUNNING) but joins the parts into one line and
// returns "" when nothing applies.
func annotationStderr(query string, r resolvedMatch, isIDResolution bool, procs []runningProc, bin string, args []string) string {
	var parts []string
	if *infoFlag {
		parts = append(parts, infoAnnotation(query, r, isIDResolution, false))
	}
	if *runningFlag {
		if m := runningMarker(procs, bin, args); m != "" {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, "   ")
}

// infoAnnotation builds the -info comment for a match: session size, and — for a
// content query — the occurrence count and a sparkline of when the query matched
// across the session's timeline, then the last-active time. An id-resolution (a
// claude.ai URL or it2 SID that resolved to a specific session) has no
// meaningful occurrences-over-time, so it shows size and time only.
// Compact, space-separated tokens: <msgs>× <lines>L <hits>@ <dense>% <spark>
// <age> <name>. Counts use distinct markers (× messages, L lines, @ hits) so
// the relative age (3h, 40m) is never confused with a count; numbers are
// humanized (2123 -> 2.1k). Unlike the aligned listing path, tokens here are
// unpadded — this feeds the single-line stderr annotation and tests.
func infoAnnotation(query string, r resolvedMatch, isIDResolution, on bool) string {
	row := annotationRows(query, []resolvedMatch{r}, []string{""}, isIDResolution)[0]
	parts := []string{row.msgs}
	if row.lines != "" {
		parts = append(parts, row.lines)
	}
	if row.hits != "" {
		// Density and sparkline form one cyan cell: the shape of relevance —
		// central vs incidental, and when.
		parts = append(parts, row.hits, hl(row.dense+" "+row.spark, ansiCyan, on))
	}
	age := row.age
	if row.recent {
		// A fresh session is likely the one being hunted for; brighten it.
		age = hl(age, ansiGreen, on)
	}
	parts = append(parts, age)
	if row.name != "" {
		if on {
			parts = append(parts, hl(" "+row.name+" ", ansiReverse, on))
		} else {
			parts = append(parts, strconv.Quote(row.name))
		}
	}
	return strings.Join(parts, " ")
}

// relAge renders how long ago t was, compactly: "20m", "3h", "2d", "5w". A zero
// or future time renders "now".
func relAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d"
	default:
		return strconv.Itoa(int(d.Hours()/(24*7))) + "w"
	}
}

// humanize renders a count compactly: 896 -> "896", 2123 -> "2.1k",
// 79853 -> "80k", 1_200_000 -> "1.2M".
func humanize(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "k"
	case n < 1_000_000:
		return strconv.Itoa((n+500)/1000) + "k"
	case n < 10_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	default:
		return strconv.Itoa((n+500_000)/1_000_000) + "M"
	}
}

// dim wraps s in ANSI dim styling when on is true, leaving it plain otherwise.
func dim(s string, on bool) string {
	if !on {
		return s
	}
	return ansiDim + s + ansiReset
}

// hl styles a token that lives inside a dim-wrapped annotation: it applies the
// given ANSI style at full intensity, then restores dim so the surrounding
// annotation text stays subdued. With on false it returns s unchanged.
func hl(s, style string, on bool) string {
	if !on {
		return s
	}
	return ansiReset + style + s + ansiReset + ansiDim
}

var sparkGlyphs = []rune("▁▂▃▄▅▆▇█")

// tsPattern extracts the leading RFC3339 timestamp string from a transcript
// line without a full JSON unmarshal.
var tsPattern = regexp.MustCompile(`"timestamp":"([^"]+)"`)

// occurrenceInfo reads the transcript once and returns a buckets-wide sparkline
// of query match density over the session's [first,last] timestamp span, plus
// the density: the percentage of timestamped lines that contain at least one
// match. Bucketing is by time (not line) so idle gaps read correctly: a burst in
// the last minutes of a long session lands in the rightmost bucket. Lines
// without a parseable timestamp are skipped. When there are no matches it
// returns an empty sparkline and zero density.
func occurrenceInfo(query, path string, buckets int) (spark string, densePct int) {
	counts, densePct := occurrenceBuckets(query, path, buckets)
	return renderSpark(counts), densePct
}

// occurrenceBuckets is occurrenceInfo before glyph rendering: the raw
// per-bucket match counts, for callers that grade each bucket visually.
// It returns a nil slice when there are no matches.
func occurrenceBuckets(query, path string, buckets int) (counts []int, densePct int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0
	}
	type lineHit struct {
		ts   time.Time
		hits int
	}
	var hits []lineHit
	var first, last time.Time
	totalLines, hitLines := 0, 0
	// The session span is defined by every timestamped line, not only matched
	// ones, so a match at the very end renders in the last bucket even when it is
	// the sole match. Density is over timestamped lines, the sparkline's own
	// denominator.
	for _, line := range strings.Split(string(data), "\n") {
		m := tsPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		totalLines++
		if first.IsZero() || ts.Before(first) {
			first = ts
		}
		if ts.After(last) {
			last = ts
		}
		if n := countOccurrences(query, line); n > 0 {
			hitLines++
			hits = append(hits, lineHit{ts: ts, hits: n})
		}
	}
	if len(hits) == 0 {
		return nil, 0
	}
	if totalLines > 0 {
		densePct = (100*hitLines + totalLines/2) / totalLines // rounded
	}
	counts = make([]int, buckets)
	span := last.Sub(first)
	for _, h := range hits {
		b := 0
		if span > 0 {
			b = int(h.ts.Sub(first) * time.Duration(buckets) / span)
			if b >= buckets {
				b = buckets - 1
			}
		}
		counts[b] += h.hits
	}
	return counts, densePct
}

// renderSpark maps bucket counts to block glyphs, scaled to the max bucket.
func renderSpark(counts []int) string {
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		if max == 0 {
			b.WriteRune(sparkGlyphs[0])
			continue
		}
		idx := c * (len(sparkGlyphs) - 1) / max
		b.WriteRune(sparkGlyphs[idx])
	}
	return b.String()
}

// renderSparkStyled is renderSpark with each bucket color-graded by intensity
// for styled output: empty buckets keep the surrounding dim, active buckets
// are cyan, and peak buckets bold cyan — so both the shape and the hotspot
// read at a glance. The visible glyphs match renderSpark exactly.
func renderSparkStyled(counts []int) string {
	max := 0
	for _, c := range counts {
		if c > max {
			max = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		if max == 0 {
			b.WriteRune(sparkGlyphs[0])
			continue
		}
		g := string(sparkGlyphs[c*(len(sparkGlyphs)-1)/max])
		switch {
		case c == 0:
			b.WriteString(g)
		case c == max:
			b.WriteString(ansiReset + ansiBoldCyan + g + ansiReset + ansiDim)
		default:
			b.WriteString(ansiReset + ansiCyan + g + ansiReset + ansiDim)
		}
	}
	return b.String()
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

// normalizeClaudeURL reduces a claude.ai URL to the bare identifier a session
// transcript contains. An artifact URL yields its UUID, a session link yields
// session_<id>, and a design link yields its slug; the query string and
// fragment are discarded. Any other query passes through unchanged.
func normalizeClaudeURL(query string) string {
	if !strings.HasPrefix(query, "https://claude.ai/") && !strings.HasPrefix(query, "http://claude.ai/") {
		return query
	}
	u, err := url.Parse(query)
	if err != nil || u.Host != "claude.ai" {
		return query
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	// The identifier is the last path segment for the known forms:
	//   /code/artifact/<uuid>, /public/artifacts/<uuid>,
	//   /code/session_<id>, /design/<slug>.
	switch {
	case len(segs) >= 3 && segs[0] == "code" && segs[1] == "artifact":
		return segs[2]
	case len(segs) >= 3 && segs[0] == "public" && segs[1] == "artifacts":
		return segs[2]
	case len(segs) >= 2 && segs[0] == "code" && strings.HasPrefix(segs[1], "session_"):
		return segs[1]
	case len(segs) >= 2 && segs[0] == "design":
		return segs[1]
	}
	return query
}

// it2SIDPattern matches an iTerm2 session ID: a bare UUID, or the wNtNpN:UUID
// form that $ITERM_SESSION_ID carries.
var it2SIDPattern = regexp.MustCompile(`^(?:w\d+t\d+p\d+:)?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// it2 pane-ID markers as they appear in transcripts and terminals:
//   - a tool-result bracket, [it2:send-text src=X dst=Y] (also send-key, etc.)
//   - a collab-msg header, <collab-msg src="X">
//
// The SID is a short 8-hex prefix or a full UUID, optionally wNtNpN:-prefixed;
// it2bind.SessionsForSID tolerates all three forms.
var (
	it2SrcRe = regexp.MustCompile(`(?:\[it2:[^\]]*\bsrc=|<collab-msg[^>]*\bsrc=")(w\d+t\d+p\d+:)?([0-9A-Fa-f-]{8,36})`)
	it2DstRe = regexp.MustCompile(`\[it2:[^\]]*\bdst=(w\d+t\d+p\d+:)?([0-9A-Fa-f-]{8,36})`)
)

// extractIt2SID reduces a copied it2 log line or collab-msg header to the bare
// pane ID for the chosen side ("src" or "dst"), which resolveIt2SID then turns
// into a session. Only the first marker in the query is used. A dst request
// with no dst present falls back to src. A query with no recognized marker
// (a URL, plain text, or an already-bare SID) is returned unchanged, so this is
// safe to run before resolveIt2SID regardless of input.
func extractIt2SID(query, side string) string {
	if side == "dst" {
		if m := it2DstRe.FindStringSubmatch(query); m != nil {
			return m[2]
		}
		// No dst (e.g. a collab-msg header); fall back to the sender.
	}
	if m := it2SrcRe.FindStringSubmatch(query); m != nil {
		return m[2]
	}
	return query
}

// resolveIt2SID rewrites an iTerm2 session ID query to the harness session most
// recently bound to that pane, using the per-pane binding events under
// ~/.it2/sessions. If the query is not an it2 SID, or no binding exists (e.g.
// the SessionStart hook never ran for that pane), the query passes through
// unchanged so the normal content search still applies.
func resolveIt2SID(query string) string {
	if !it2SIDPattern.MatchString(query) {
		return query
	}
	root, ok := it2bind.DefaultRoot()
	if !ok {
		fmt.Fprintln(os.Stderr, "ccresume: cannot locate it2 event store; searching transcript content")
		return query
	}
	return resolveIt2SIDInStore(query, root, os.Stderr)
}

func resolveIt2SIDInStore(query, root string, diagnostics io.Writer) string {
	if !it2SIDPattern.MatchString(query) {
		return query
	}
	intervals, err := it2bind.Load(root)
	if err == nil {
		if sessions := it2bind.SessionsForSID(intervals, query); len(sessions) > 0 {
			return sessions[0]
		}
	}
	_, verdicts := cchooks.ReadStore(root)
	for _, d := range verdicts {
		fmt.Fprintf(diagnostics, "ccresume: %s: %s\n", d.Code, d.Message)
	}
	if err != nil {
		fmt.Fprintf(diagnostics, "ccresume: read it2 bindings: %v\n", err)
	}
	fmt.Fprintf(diagnostics, "ccresume: no binding for it2 pane %s; searching transcript content\n", query)
	return query
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
	// Transcripts reach hundreds of megabytes; let rg fold the case rather
	// than allocating a lowered copy of the whole file.
	if n, ok := rgCountMatches(contains, path); ok {
		return n > 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, nil
	}
	return containsFold(string(data), contains), nil
}

// containsFold reports whether s contains substr, ignoring case. The filters
// take recalled text, so they match the way the search that found the session
// does.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
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
	entries, err := cc.ReadFile(context.Background(), path)
	if err != nil {
		return false, nil
	}
	cmdIDs := map[string]bool{}
	sawCommand := cmdText == ""
	sawResult := resultText == ""
	for _, e := range entries {
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
	return sawCommand && sawResult, nil
}

func toolUseContains(tu cc.ContentBlock, text string) bool {
	if text == "" {
		return true
	}
	if containsFold(tu.BashCommand(), text) {
		return true
	}
	return containsFold(string(tu.Input), text)
}

func toolResultContains(tr cc.ContentBlock, text string) bool {
	if text == "" {
		return true
	}
	return containsFold(tr.Content, text) || containsFold(string(tr.Input), text)
}

func toolUseResultContains(res *cc.ToolUseResult, text string) bool {
	if text == "" {
		return true
	}
	return containsFold(res.Stdout, text) ||
		containsFold(res.Stderr, text) ||
		containsFold(res.Error, text) ||
		containsFold(res.Text, text) ||
		containsFold(string(res.Content), text)
}

// allHistory is the window the widening fallback searches with: longer than
// any session's age, so the mtime bound selects every candidate file.
const allHistory = 100 * 365 * 24 * time.Hour

// sinceSet reports whether -since was given on the command line.
func sinceSet() bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "since" {
			set = true
		}
	})
	return set
}

// searchSpec is one ccresume query: the text to look for and the filters that
// narrow it. Holding it together lets the same search run twice, once bounded
// by -since and again over all history when that bound hides everything.
type searchSpec struct {
	query      string
	has        string
	cmdText    string
	resultText string
	wrote      string
	dir        string
	harness    string
}

// run returns the sessions matching the spec among those modified within since.
func (s searchSpec) run(since time.Duration) ([]cc.IndexEntry, error) {
	// The metadata scan and the content grep look at disjoint data, and the
	// grep is only needed when the metadata misses; both walk the same recent
	// files, so scan once alongside the index read and use the grep only as a
	// fallback.
	contentQuery := s.query
	switch {
	case filepath.IsAbs(s.query):
		contentQuery = jsonCWDField(s.query)
	case s.query == s.wrote:
		// A -wrote pattern is a path or glob, not free text; narrow candidates
		// with a since-bounded literal grep (the dedicated block below then
		// checks which actually wrote a matching file). Never fall through to
		// the unbounded grepMatches, which would scan all history for a glob.
		contentQuery = literalToken(s.wrote)
	}
	// Bound the scan by -since: candidate files are selected by mtime before
	// anything reads them, so old history is never scanned or resolved.
	scanCh := make(chan scanResult, 1)
	go func() {
		scanCh <- scanRecentSessions(s.query, contentQuery, since)
	}()

	matches, err := findMatches(s.query, since)
	if err != nil {
		return nil, err
	}
	res := <-scanCh
	if res.err != nil {
		return nil, res.err
	}
	// Claude Code stopped writing sessions-index.json, and codex never had one,
	// so the index alone reports neither harness. The metadata scan derives the
	// same fields from the session files themselves; merge the two so an
	// indexed match from one harness cannot hide a live session from another.
	matches = mergeEntries(matches, res.meta)
	if len(matches) == 0 {
		matches = res.content
	}
	if s.has != "" {
		matches, err = filterContentMatches(matches, s.has)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 && s.query == s.has {
			matches, err = grepContentMatches(s.has, since)
			if err != nil {
				return nil, err
			}
		}
	}
	if s.cmdText != "" || s.resultText != "" {
		matches, err = filterToolMatches(matches, s.cmdText, s.resultText)
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 && (s.query == s.cmdText || s.query == s.resultText) {
			matches, err = grepToolMatches(s.query, s.cmdText, s.resultText, since)
			if err != nil {
				return nil, err
			}
		}
	}
	if s.wrote != "" {
		matches = filterWroteMatches(matches, s.wrote)
		// When -wrote is the only filter (so it drove the query), the metadata and
		// literal-grep passes above cannot find a glob pattern; do the real
		// structured scan over recent sessions.
		if len(matches) == 0 && s.query == s.wrote {
			matches, err = grepWroteMatches(s.wrote, since)
			if err != nil {
				return nil, err
			}
		}
	}
	matches = filterMatches(matches, s.dir, s.harness)

	return matches, nil
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
	ch, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, err
	}
	roots := []searchRoot{{dir: filepath.Join(ch, "projects"), kind: "claude"}}

	gh, _ := ccpaths.GeminiHome()
	if gh != "" {
		if d := filepath.Join(gh, "projects"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "gemini"})
		}
	}
	xh, _ := ccpaths.CodexHome()
	if xh != "" {
		if d := filepath.Join(xh, "sessions"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "codex"})
		}
	}
	oh, _ := ccpaths.OpenCodeHome()
	if oh != "" {
		d := filepath.Join(oh, "storage", "session")
		if dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "opencode"})
		}
	}
	ph, _ := ccpaths.PiHome()
	if ph != "" {
		if d := filepath.Join(ph, "sessions"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "pi"})
		}
	}
	ah, _ := ccpaths.AntigravityHome()
	if ah != "" {
		if d := filepath.Join(ah, "brain"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "antigravity"})
		}
	}
	if gh != "" {
		if d := filepath.Join(gh, "antigravity-cli", "brain"); dirExists(d) {
			roots = append(roots, searchRoot{dir: d, kind: "antigravity"})
		}
	}
	return roots, nil
}

// scanResult holds the two passes scanRecentSessions makes over one set of
// recently modified session files.
type scanResult struct {
	meta    []cc.IndexEntry
	content []cc.IndexEntry
	err     error
}

// scanRecentSessions walks the session roots once and returns both the entries
// whose metadata matches query and those whose transcript contains
// contentQuery. Walking once matters: the trees hold tens of thousands of
// files, and the two passes want the same mtime-bounded candidate set.
func scanRecentSessions(query, contentQuery string, since time.Duration) scanResult {
	roots, err := sessionRoots()
	if err != nil {
		return scanResult{err: err}
	}
	lists, err := recentCandidates(roots, sinceMinutes(since))
	if err != nil {
		return scanResult{err: err}
	}
	content, err := contentMatches(contentQuery, roots, lists)
	if err != nil {
		return scanResult{err: err}
	}
	return scanResult{meta: metadataMatches(query, roots, lists), content: content}
}

// sinceMinutes converts a -since duration to the whole minutes the file walk
// uses, never rounding down to zero.
func sinceMinutes(since time.Duration) int {
	if m := int(since.Minutes()); m >= 1 {
		return m
	}
	return 1
}

// metadataMatches returns the candidate sessions whose identity, session ID or
// project path, contains query. It covers only the harnesses AllIndexEntries
// misses: Claude Code no longer writes sessions-index.json and codex has no
// index at all. opencode, pi, and antigravity are already scanned (and cached)
// there, and deriving them again here would reparse every transcript.
func metadataMatches(query string, roots []searchRoot, lists [][]string) []cc.IndexEntry {
	type candidate struct {
		root searchRoot
		path string
	}
	var cands []candidate
	for i, root := range roots {
		switch root.kind {
		case "claude", "gemini", "codex":
		default:
			continue
		}
		for _, path := range lists[i] {
			if isResumeSessionPath(path, root.kind) {
				cands = append(cands, candidate{root, path})
			}
		}
	}

	// A codex candidate costs a header read, so resolve them concurrently.
	q := strings.ToLower(query)
	entries := make([]cc.IndexEntry, len(cands))
	hit := make([]bool, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			info, err := os.Stat(c.path)
			if err != nil {
				return
			}
			e, ok := indexEntryForPath(c.path, c.root, info)
			if !ok || !containsAny(q, e.SessionID, e.ProjectPath) {
				return
			}
			entries[i], hit[i] = e, true
		}()
	}
	wg.Wait()

	var matches []cc.IndexEntry
	for i := range entries {
		if hit[i] {
			matches = append(matches, entries[i])
		}
	}
	return matches
}

// mergeEntries appends the entries of b that a does not already hold, keeping
// a's richer indexed fields for a session both passes found.
func mergeEntries(a, b []cc.IndexEntry) []cc.IndexEntry {
	seen := make(map[string]bool, len(a))
	for _, e := range a {
		seen[e.FullPath] = true
	}
	for _, e := range b {
		if seen[e.FullPath] {
			continue
		}
		seen[e.FullPath] = true
		a = append(a, e)
	}
	return a
}

func recentContentMatches(query string, since time.Duration) ([]cc.IndexEntry, error) {
	roots, err := sessionRoots()
	if err != nil {
		return nil, err
	}
	lists, err := recentCandidates(roots, sinceMinutes(since))
	if err != nil {
		return nil, err
	}
	return contentMatches(query, roots, lists)
}

// contentMatches greps the candidate files for query and resolves the hits to
// session entries.
func contentMatches(query string, roots []searchRoot, lists [][]string) ([]cc.IndexEntry, error) {
	paths, err := grepCandidates(query, lists)
	if err != nil {
		return nil, err
	}
	var matches []cc.IndexEntry
	seen := map[string]bool{}
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
		if !isResumeSessionPath(path, rootMatch.kind) {
			continue
		}
		if e, ok := indexEntryForPath(path, rootMatch, info); ok {
			if seen[e.FullPath] {
				continue
			}
			seen[e.FullPath] = true
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
	entries, err := cc.ReadFile(context.Background(), path)
	if err != nil {
		return false, nil
	}
	for _, e := range entries {
		if e.CWD == cwd && (e.Subtype == "session_meta" || e.Subtype == "turn_context" || e.Type == "user" || e.Type == "assistant") {
			return true, nil
		}
	}
	return false, nil
}

func recentMatchingPaths(roots []searchRoot, query string, minutes int) ([]string, error) {
	lists, err := recentCandidates(roots, minutes)
	if err != nil {
		return nil, err
	}
	return grepCandidates(query, lists)
}

// recentCandidates lists, per root, the session files modified within the last
// minutes.
func recentCandidates(roots []searchRoot, minutes int) ([][]string, error) {
	// One find per root, concurrently: a single find serializes behind the
	// slowest tree (antigravity's brain/ is by far the largest).
	lists := make([][]string, len(roots))
	errs := make([]error, len(roots))
	var wg sync.WaitGroup
	for i, root := range roots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i], errs[i] = recentFiles(root, minutes)
		}()
	}
	wg.Wait()
	for i := range roots {
		if errs[i] != nil {
			return nil, errs[i]
		}
	}
	return lists, nil
}

// grepCandidates returns the candidate files that contain the literal query.
func grepCandidates(query string, lists [][]string) ([]string, error) {
	var paths []string
	for _, list := range lists {
		paths = append(paths, list...)
	}
	var wg sync.WaitGroup

	// Grep the candidates in parallel batches: rg is fast per file, but a
	// serial walk over thousands of transcripts is not.
	const batchSize = 200
	var batches [][]string
	for len(paths) > 0 {
		n := batchSize
		if len(paths) < n {
			n = len(paths)
		}
		batches = append(batches, paths[:n])
		paths = paths[n:]
	}
	found := make([][]string, len(batches))
	berrs := make([]error, len(batches))
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, batch := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			found[i], berrs[i] = grepFiles(query, batch)
		}()
	}
	wg.Wait()
	var matches []string
	for i := range batches {
		if berrs[i] != nil {
			return nil, berrs[i]
		}
		matches = append(matches, found[i]...)
	}
	return matches, nil
}

// recentFiles lists the session files under root modified within the last
// minutes. Antigravity's chunked transcripts are skipped: they duplicate the
// conversation's transcript.jsonl, which resolves to the same session.
func recentFiles(root searchRoot, minutes int) ([]string, error) {
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute)
	if root.kind == "antigravity" {
		return antigravityRecentFiles(root.dir, cutoff), nil
	}

	// The trees are wide and shallow (one directory per project), and the walk
	// is stat-bound, so scan the top-level directories concurrently.
	top, err := os.ReadDir(root.dir)
	if err != nil {
		return nil, nil
	}
	var files []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2*runtime.GOMAXPROCS(0))
	add := func(paths []string) {
		if len(paths) == 0 {
			return
		}
		mu.Lock()
		files = append(files, paths...)
		mu.Unlock()
	}
	for _, e := range top {
		name := e.Name()
		path := filepath.Join(root.dir, name)
		if !e.IsDir() {
			if info, err := e.Info(); err == nil && isRecentSessionFile(name, info, cutoff) {
				add([]string{path})
			}
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			add(walkRecentFiles(path, cutoff))
		}()
	}
	wg.Wait()
	return files, nil
}

// antigravityRecentFiles lists the recently modified transcripts under an
// antigravity brain directory. A conversation keeps its transcripts in one
// known place, brain/<id>/.system_generated/logs, so read those directly:
// walking a conversation costs hundreds of stats for the artifacts it holds,
// and the chunks/ subdirectory only duplicates transcript.jsonl.
func antigravityRecentFiles(root string, cutoff time.Time) []string {
	convs, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var files []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 2*runtime.GOMAXPROCS(0))
	for _, c := range convs {
		if !c.IsDir() {
			continue
		}
		logs := filepath.Join(root, c.Name(), ".system_generated", "logs")
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ents, err := os.ReadDir(logs)
			if err != nil {
				return
			}
			var found []string
			for _, e := range ents {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil || !isRecentSessionFile(e.Name(), info, cutoff) {
					continue
				}
				found = append(found, filepath.Join(logs, e.Name()))
			}
			if len(found) > 0 {
				mu.Lock()
				files = append(files, found...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return files
}

// walkRecentFiles returns the session files under dir modified since cutoff.
func walkRecentFiles(dir string, cutoff time.Time) []string {
	var files []string
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range ents {
		name := e.Name()
		path := filepath.Join(dir, name)
		if e.IsDir() {
			files = append(files, walkRecentFiles(path, cutoff)...)
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if isRecentSessionFile(name, info, cutoff) {
			files = append(files, path)
		}
	}
	return files
}

func isRecentSessionFile(name string, info os.FileInfo, cutoff time.Time) bool {
	if !info.Mode().IsRegular() || info.ModTime().Before(cutoff) {
		return false
	}
	return strings.HasSuffix(name, ".jsonl") ||
		(strings.HasPrefix(name, "ses_") && strings.HasSuffix(name, ".json"))
}

// grepFiles returns the files among paths that contain the literal query,
// ignoring case. A query is usually recalled text - a part number, an error
// string - whose original capitalization the user has no reason to remember,
// and every filter and count downstream already folds case.
func grepFiles(query string, paths []string) ([]string, error) {
	args := append([]string{"-l", "-F", "-i", "--", query}, paths...)
	out, err := exec.Command("rg", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("rg: %w", err)
	}
	return splitLines(out), nil
}

func splitLines(out []byte) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		if t := scanner.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	return lines
}

func indexEntryForPath(path string, root searchRoot, info os.FileInfo) (cc.IndexEntry, bool) {
	if !isResumeSessionPath(path, root.kind) {
		return cc.IndexEntry{}, false
	}
	if root.kind == "claude" || root.kind == "gemini" {
		// Resumable transcripts live directly in a project directory. Nested
		// files belong to subagents, workflows, or other supporting data.
		rel, err := filepath.Rel(root.dir, path)
		if err != nil || len(strings.Split(rel, string(filepath.Separator))) != 2 {
			return cc.IndexEntry{}, false
		}
	}
	sessionID := sessionIDFromPath(path, root.kind)
	projectPath := filepath.Dir(path)
	modified := info.ModTime()
	switch root.kind {
	case "codex":
		// codex files rollouts by date, so the directory says nothing about
		// the project; the session_meta header on the first line is the only
		// record of where the session ran and whether it is a subagent.
		h, ok := codexSessionHeader(path)
		if !ok || h.id == "" || h.subagent {
			return cc.IndexEntry{}, false
		}
		sessionID = h.id
		if h.cwd != "" {
			projectPath = h.cwd
		}
	case "opencode":
		var v struct {
			ID        string `json:"id"`
			Directory string `json:"directory"`
		}
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, &v) != nil || v.ID == "" {
			return cc.IndexEntry{}, false
		}
		sessionID = v.ID
		if v.Directory != "" {
			projectPath = v.Directory
		}
	case "pi":
		// A pi transcript starts with a session header, even when empty.
		id, cwd, ok := piSessionHeader(path)
		if !ok || id == "" {
			return cc.IndexEntry{}, false
		}
		sessionID = id
		if cwd != "" {
			projectPath = cwd
		}
	case "antigravity":
		// Antigravity sessions are directories under brain/<conversation-uuid>.
		// rg may match a transcript or artifact file nested inside; resolve to
		// the brain session directory and use the reader to recover id and cwd.
		convID := antigravityConversationIDFromPath(path)
		if convID == "" {
			return cc.IndexEntry{}, false
		}
		sessionDir := filepath.Join(root.dir, convID)
		path = sessionDir
		sessionID = "ag-" + convID
		projectPath = sessionDir
		if entries, err := cc.ReadFile(context.Background(), sessionDir); err == nil {
			s := cc.Summarize(sessionDir, entries)
			if s.SessionID != "" {
				sessionID = s.SessionID
			}
			if s.CWD != "" {
				projectPath = s.CWD
			}
			if !s.LastTime.IsZero() {
				modified = s.LastTime
			}
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
		Modified:    modified.Format(time.RFC3339Nano),
	}, true
}

func isResumeSessionPath(path, kind string) bool {
	base := filepath.Base(path)
	switch kind {
	case "claude", "gemini":
		return strings.HasSuffix(base, ".jsonl") && base != "journal.jsonl"
	case "codex", "pi":
		return strings.HasSuffix(base, ".jsonl")
	case "opencode":
		return strings.HasPrefix(base, "ses_") && strings.HasSuffix(base, ".json")
	case "antigravity":
		return (base == "transcript.jsonl" || base == "transcript_full.jsonl") &&
			filepath.Base(filepath.Dir(path)) == "logs" &&
			filepath.Base(filepath.Dir(filepath.Dir(path))) == ".system_generated" &&
			antigravityConversationIDFromPath(path) != ""
	}
	return false
}

func sessionIDFromPath(path, kind string) string {
	base := filepath.Base(path)
	if kind == "opencode" && strings.HasPrefix(base, "ses_") && strings.HasSuffix(base, ".json") {
		return strings.TrimSuffix(base, ".json")
	}
	id := strings.TrimSuffix(base, ".jsonl")
	if kind == "pi" {
		// pi names files <ISO-timestamp>_<uuid>.jsonl; the uuid is the session
		// id. Fall back to it when the header cannot be read.
		if i := strings.LastIndex(id, "_"); i >= 0 && i+1 < len(id) {
			id = id[i+1:]
		}
	}
	return id
}

// isPiResumePath reports whether a session path is a pi session, by its default
// ~/.pi/ location or the agent/sessions layout used under PI_CODING_AGENT_DIR.
func isPiResumePath(path string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(path, sep+".pi"+sep) ||
		strings.Contains(path, sep+"agent"+sep+"sessions"+sep)
}

// isAntigravityResumePath reports whether a session path is an Antigravity (agy)
// session, by the "brain" directory that holds per-conversation session dirs.
func isAntigravityResumePath(path string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(path, sep+"brain"+sep) ||
		strings.HasSuffix(path, sep+"brain")
}

// antigravityConversationID returns the bare conversation UUID used by
// "agy --conversation". It prefers the indexed SessionID (stripping the "ag-"
// prefix added by the reader) and falls back to the brain session directory
// name derived from the path.
func antigravityConversationID(m cc.IndexEntry) string {
	if id := strings.TrimPrefix(m.SessionID, "ag-"); id != "" {
		return id
	}
	return antigravityConversationIDFromPath(m.FullPath)
}

// antigravityConversationIDFromPath extracts the brain session directory name
// (the conversation UUID) from an Antigravity session path. The path may be the
// session directory itself or a transcript file nested under it.
func antigravityConversationIDFromPath(path string) string {
	sep := string(filepath.Separator)
	_, rest, ok := strings.Cut(path, sep+"brain"+sep)
	if !ok {
		return ""
	}
	if id, _, ok := strings.Cut(rest, sep); ok {
		return id
	}
	return rest
}

// codexHeader is what a rollout's session_meta line says about the thread.
type codexHeader struct {
	id       string
	cwd      string
	subagent bool
}

// codexSessionHeader reads the session_meta line that opens a codex rollout
// file. A subagent rollout carries its parent's session_id, so the thread's own
// id is read from payload.id.
func codexSessionHeader(path string) (codexHeader, bool) {
	f, err := os.Open(path)
	if err != nil {
		return codexHeader{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), cc.MaxLineSize)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var h struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID    string `json:"session_id"`
				ID           string `json:"id"`
				CWD          string `json:"cwd"`
				ThreadSource string `json:"thread_source"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &h) != nil || h.Type != "session_meta" {
			return codexHeader{}, false
		}
		id := h.Payload.ID
		if id == "" {
			id = h.Payload.SessionID
		}
		return codexHeader{id: id, cwd: h.Payload.CWD, subagent: h.Payload.ThreadSource == "subagent"}, true
	}
	return codexHeader{}, false
}

// piSessionHeader reads the first line of a pi session file and returns its
// session id and cwd.
func piSessionHeader(path string) (id, cwd string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), cc.MaxLineSize)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var h struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			CWD  string `json:"cwd"`
		}
		if json.Unmarshal([]byte(line), &h) != nil || h.Type != "session" {
			return "", "", false
		}
		return h.ID, h.CWD, true
	}
	return "", "", false
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
	if result, ok := ccpaths.DecodeSegments("/"+segments[0], segments[1:]); ok {
		return result
	}
	return "/" + strings.Join(segments, "/")
}

// validSessionID reports whether a session ID is a valid resume target.
// Subagent transcripts (agent-<id>, including the agent-acompact-* compaction
// runs) are spawned by a parent session and cannot be resumed on their own, so
// they are never offered as results.
func validSessionID(id string) bool {
	return id != "" && !strings.HasPrefix(id, "agent-")
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
	case strings.Contains(m.FullPath, string(filepath.Separator)+"opencode"+string(filepath.Separator)):
		if m.SessionID != "" {
			return "opencode", []string{"--session", m.SessionID}
		}
		return "opencode", nil
	case isAntigravityResumePath(m.FullPath):
		// Antigravity resumes a conversation by its bare UUID, which is the
		// brain session directory name. This must be checked before .gemini
		// since the default Antigravity home lives under ~/.gemini.
		if id := antigravityConversationID(m); id != "" {
			return "agy", []string{"--conversation", id}
		}
		return "agy", nil
	case strings.Contains(m.FullPath, string(filepath.Separator)+".gemini"+string(filepath.Separator)):
		if m.SessionID != "" {
			return "gemini", []string{"-r", m.SessionID}
		}
		return "gemini", nil
	case isPiResumePath(m.FullPath):
		// pi resumes by session file path, not by id.
		return "pi", []string{"--session", m.FullPath}
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
	return fmt.Sprintf("cd %s; %s", homeQuote(projectPath), cmd)
}

// homeQuote renders a directory for a cd command, abbreviating a leading $HOME
// to ~ (which the shell still expands) and shell-quoting the remainder. The ~
// stays outside the quotes so it is not treated literally: cd ~/'a b' expands
// to $HOME/a b.
func homeQuote(dir string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if dir == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(dir, home+string(os.PathSeparator)); ok {
			return "~/" + shellQuote(rest)
		}
	}
	return shellQuote(dir)
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
