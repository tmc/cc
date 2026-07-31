package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
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

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass/it2bind"
	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/ccpaths"
)

var (
	launchFlag  = flag.Bool("l", false, "Launch claude instead of printing command")
	sinceFlag   = flag.String("since", "7d", "Search sessions modified within duration")
	oneFlag     = flag.Bool("1", false, "Show only the most recent match")
	clipFlag    = flag.Bool("clip", true, "Use clipboard as search query (pbpaste)")
	pathsFlag   = flag.Bool("paths", false, "Print raw session file paths instead of resume commands")
	hasFlag     = flag.String("has", "", "Only include sessions whose transcript contains this literal text")
	cmdFlag     = flag.String("cmd", "", "Only include sessions with a tool command containing this literal text")
	resultFlag  = flag.String("result", "", "Only include sessions with a tool result containing this literal text")
	wroteFlag   = flag.String("wrote", "", "Only include sessions that wrote a file matching this path or glob")
	dirFlag     = flag.String("dir", "", "Only include sessions recorded in this directory")
	harnessFlag = flag.String("harness", "", "Only include sessions from this harness")
	sidSideFlag = flag.String("sid-side", "src", "When a query is an it2 send-text line, resume the src or dst pane")
	sortFlag    = flag.String("sort", "relevance", "Order matches by relevance or recency")
	infoFlag    = flag.Bool("info", false, "Annotate each resume line with session size and an occurrence sparkline")
	runningFlag = flag.Bool("show-is-running", false, "Annotate each resume line whose session is already running as a process")
	noColorFlag = flag.Bool("no-color", false, "Disable ANSI color in -info annotations")
)

const (
	ansiDim   = "\033[2m"
	ansiReset = "\033[0m"
)

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
		case query == wrote:
			// A -wrote pattern is a path or glob, not free text; narrow candidates
			// with a since-bounded literal grep (the dedicated block below then
			// checks which actually wrote a matching file). Never fall through to
			// the unbounded grepMatches, which would scan all history for a glob.
			matches, err = recentContentMatches(literalToken(wrote), since)
			if err != nil {
				return err
			}
		default:
			// Bound the content grep by -since: find selects candidate files by
			// mtime before rg runs, so old history is never scanned or resolved.
			matches, err = recentContentMatches(query, since)
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
	if wrote != "" {
		matches = filterWroteMatches(matches, wrote)
		// When -wrote is the only filter (so it drove the query), the metadata and
		// literal-grep passes above cannot find a glob pattern; do the real
		// structured scan over recent sessions.
		if len(matches) == 0 && query == wrote {
			matches, err = grepWroteMatches(wrote, since)
			if err != nil {
				return err
			}
		}
	}
	matches = filterMatches(matches, dir, harness)

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
			fmt.Println(m.FullPath)
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

	for _, r := range resolved {
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
		// A trailing shell comment keeps the line pipe/copy-safe: sh ignores
		// everything after '#'. The comment is reference, not payload, so it is
		// dimmed on a terminal; piped output stays byte-clean.
		line := renderResumeCommand(r.target, bin, args)
		line = annotateLine(line, query, r, isIDResolution, procs, bin, args, useColor(os.Stdout))
		fmt.Println(line)
	}
	return nil
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
	summary, err := cc.SummarizeFile(context.Background(), m.FullPath)
	if err != nil {
		return r
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

// annotateLine appends the enabled annotations to a rendered resume command as
// trailing shell comments: first the -info size/occurrence detail, then — at the
// very end of the line — a "# RUNNING" comment for any match whose session is
// already open in a running process. Both are comments, so the line stays safe
// to pipe into sh (everything after the first '#' is ignored). on selects ANSI
// dimming. bin and args are the resume invocation, used both to match the session
// against running processes and, via resumeToken, as the identifier to look for.
func annotateLine(line, query string, r resolvedMatch, isIDResolution bool, procs []runningProc, bin string, args []string, on bool) string {
	if *infoFlag {
		line += "   " + dim("# "+infoAnnotation(query, r, isIDResolution), on)
	}
	if *runningFlag {
		if m := runningMarker(procs, bin, args); m != "" {
			line += "   " + dim("# "+m, on)
		}
	}
	return line
}

// annotationStderr builds the annotation text logged to stderr in -paths and -l
// modes, where it cannot ride on the command line. It mirrors annotateLine's
// ordering (info detail, then RUNNING) but joins the parts into one line and
// returns "" when nothing applies.
func annotationStderr(query string, r resolvedMatch, isIDResolution bool, procs []runningProc, bin string, args []string) string {
	var parts []string
	if *infoFlag {
		parts = append(parts, infoAnnotation(query, r, isIDResolution))
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
func infoAnnotation(query string, r resolvedMatch, isIDResolution bool) string {
	// MessageCount is free from the index, but the grep fallback builds entries
	// without it; fall back to the summary, which is read anyway.
	msgs := r.entry.MessageCount
	if msgs == 0 {
		msgs = r.summary.UserMessages + r.summary.AsstMessages
	}
	// Compact, space-separated tokens so the annotation fits on one line after
	// the (already long) resume command: <msgs>× <lines>L <hits>@ <dense>%
	// <spark> <age>. Counts use distinct markers (× messages, L lines, @ hits)
	// so the trailing relative age (3h, 40m) is never confused with a count.
	// Numbers are humanized (2123 -> 2.1k).
	parts := []string{humanize(msgs) + "×"}
	if r.summary.TotalLines > 0 {
		parts = append(parts, humanize(r.summary.TotalLines)+"L")
	}
	if !isIDResolution && r.occurrences > 0 {
		spark, densePct := occurrenceInfo(query, r.entry.FullPath, 8)
		// hits, then density adjacent to the sparkline: together density+spark
		// are the "shape of relevance" — central vs incidental, and when.
		parts = append(parts, humanize(r.occurrences)+"@", fmt.Sprintf("%d%% %s", densePct, spark))
	}
	parts = append(parts, relAge(r.entry.ModifiedTime()))
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
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
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
		return "", 0
	}
	if totalLines > 0 {
		densePct = (100*hitLines + totalLines/2) / totalLines // rounded
	}
	counts := make([]int, buckets)
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
	return renderSpark(counts), densePct
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
		return query
	}
	intervals, err := it2bind.Load(root)
	if err != nil {
		return query
	}
	if sessions := it2bind.SessionsForSID(intervals, query); len(sessions) > 0 {
		return sessions[0]
	}
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
		strings.Contains(res.Text, text) ||
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
	args := make([]string, 0, len(roots)+7)
	for _, root := range roots {
		args = append(args, root.dir)
	}
	args = append(args, "-type", "f", "(", "-name", "*.jsonl", "-o", "-name", "ses_*.json", ")", "-mmin", fmt.Sprintf("-%d", minutes), "-print")
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

func indexEntryForPath(path string, root searchRoot, info os.FileInfo) (cc.IndexEntry, bool) {
	sessionID := sessionIDFromPath(path, root.kind)
	projectPath := filepath.Dir(path)
	modified := info.ModTime()
	switch root.kind {
	case "codex":
		if strings.HasPrefix(sessionID, "rollout-") && len(sessionID) >= len("rollout-2006-01-02T15-04-05-")+1 {
			sessionID = strings.TrimPrefix(sessionID[len("rollout-2006-01-02T15-04-05-"):], "-")
		}
	case "opencode":
		projectPath = filepath.Dir(path)
		data, err := os.ReadFile(path)
		if err == nil {
			var v struct {
				ID        string `json:"id"`
				Directory string `json:"directory"`
			}
			if json.Unmarshal(data, &v) == nil {
				if v.ID != "" {
					sessionID = v.ID
				}
				if v.Directory != "" {
					projectPath = v.Directory
				}
			}
		}
	case "pi":
		// pi encodes the cwd into the session dir name lossily; the session
		// header's id and cwd are authoritative.
		projectPath = filepath.Dir(path)
		if id, cwd, ok := piSessionHeader(path); ok {
			if id != "" {
				sessionID = id
			}
			if cwd != "" {
				projectPath = cwd
			}
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
	if kind == "antigravity" {
		// Any file matched under brain/ resolves to its conversation directory.
		return antigravityConversationIDFromPath(path) != ""
	}
	if strings.HasSuffix(path, ".jsonl") {
		return true
	}
	return kind == "opencode" && strings.HasPrefix(filepath.Base(path), "ses_") && strings.HasSuffix(path, ".json")
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
	return !strings.HasPrefix(id, "agent-")
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
