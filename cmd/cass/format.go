package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/term"
)

// Color styles for terminal output.
var (
	agentStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("6")) // Cyan.
	titleStyle     = lipgloss.NewStyle().Bold(true)
	snippetStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("8")) // Dim gray.
	workspaceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("2")) // Green.
	countStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	timeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // Yellow.
	sendStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("4"))  // Blue.
	observeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))  // Yellow.
	nodeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))  // Magenta.
	labelStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))  // Dim.
	highlightStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // Bright yellow.
	numberStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	headerStyle    = lipgloss.NewStyle().Bold(true).Underline(true)
)

// noColor reports whether color should be disabled because stdout is not a
// terminal or NO_COLOR is set.
func noColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// termenvAscii is termenv.Ascii, the profile that renders no escapes at all.
// lipgloss takes a termenv.Profile and termenv is not a direct dependency of
// this module, so the value is spelled out. It is not zero: zero is TrueColor,
// which is what this used to select, so -no-color and a piped stdout both
// kept their colors.
const termenvAscii = 3

// configureColor disables lipgloss color output when force is true or when
// stdout is not a terminal. It must be called from main after flag parsing so
// that library users of this package are unaffected.
func configureColor(force bool) {
	if force || noColor() {
		lipgloss.SetColorProfile(termenvAscii)
	}
}

// shortProject extracts a short project name from a workspace path.
// "/Volumes/example/go/src/github.com/tmc/cc" -> "tmc/cc"
// "/home/user/my-project" -> "my-project"
func shortProject(workspace string) string {
	if workspace == "" {
		return ""
	}
	// Try to find the github.com/.../... pattern.
	if idx := strings.Index(workspace, "github.com/"); idx >= 0 {
		after := workspace[idx+len("github.com/"):]
		// Take owner/repo.
		parts := strings.SplitN(after, "/", 3)
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
		return after
	}
	// Fall back to last two path components.
	return filepath.Join(filepath.Base(filepath.Dir(workspace)), filepath.Base(workspace))
}

// termWidth reports the width to wrap to: the terminal's own width, else
// $COLUMNS, else a terminal-typical 100.
func termWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 100
}

// formatSnippet renders an FTS5 snippet, colorizing the text between the
// >>> and <<< markers and folding to width columns. Continuation lines are
// indented by indent spaces to line up under the first.
func formatSnippet(s string, width, indent int) string {
	if width < 20 {
		width = 20
	}
	var b strings.Builder
	col := 0
	for _, w := range snippetWords(s) {
		n := len([]rune(w.plain))
		switch {
		case col == 0:
		case col+1+n <= width:
			b.WriteString(" ")
			col++
		default:
			b.WriteString("\n" + strings.Repeat(" ", indent))
			col = 0
		}
		b.WriteString(w.rendered)
		col += n
	}
	return b.String()
}

// snippetWord is one whitespace-delimited word of a snippet: plain is its
// text without markers, rendered is the same text with match spans colored.
type snippetWord struct {
	plain    string
	rendered string
}

// snippetWords splits s into words, stripping the FTS5 markers and coloring
// the text they enclosed. An unterminated >>> runs to the end: FTS5 can
// truncate mid-span at the ellipsis boundary, and the tail still matched.
func snippetWords(s string) []snippetWord {
	var words []snippetWord
	var w snippetWord
	seg, match := "", false
	endSeg := func() {
		if seg == "" {
			return
		}
		if match {
			w.rendered += highlightStyle.Render(seg)
		} else {
			w.rendered += snippetStyle.Render(seg)
		}
		w.plain += seg
		seg = ""
	}
	endWord := func() {
		endSeg()
		if w.plain != "" {
			words = append(words, w)
		}
		w = snippetWord{}
	}
	for i := 0; i < len(s); {
		switch {
		case strings.HasPrefix(s[i:], ">>>"):
			endSeg()
			match, i = true, i+3
		case strings.HasPrefix(s[i:], "<<<"):
			endSeg()
			match, i = false, i+3
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			endWord()
			i++
		default:
			seg += s[i : i+1]
			i++
		}
	}
	endWord()
	return words
}

// durationStyle renders session duration.
var durationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5")) // Magenta.

// formatDuration formats seconds as a compact human-readable duration.
// 0 -> "", 45 -> "45s", 300 -> "5m", 3900 -> "1h 5m", 7200 -> "2h"
func formatDuration(secs int) string {
	if secs <= 0 {
		return ""
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// formatBytes formats a byte count as a compact human-readable string.
func formatBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1fkB", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// formatTokens formats a token count as a compact string (e.g. 15.2K, 3.1M).
func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// relativeTime formats a duration since a timestamp as a human-readable string.
func relativeTime(ts string) string {
	if ts == "" {
		return ""
	}
	t, err := parseTime(ts)
	if err != nil {
		return ts
	}
	d := timeSince(t)
	switch {
	case d.Hours() < 1:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d.Hours() < 24:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d.Hours() < 168:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return ts[:10] // Just the date.
	}
}
