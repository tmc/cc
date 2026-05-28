package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
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

// configureColor disables lipgloss color output when force is true or when
// stdout is not a terminal. It must be called from main after flag parsing so
// that library users of this package are unaffected.
func configureColor(force bool) {
	if force || noColor() {
		lipgloss.SetColorProfile(0) // No color.
	}
}

// shortProject extracts a short project name from a workspace path.
// "/Volumes/tmc/go/src/github.com/tmc/cc" -> "tmc/cc"
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

// formatSnippet highlights FTS5 snippet markers (>>> and <<<) with color.
func formatSnippet(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 120 {
		s = s[:120] + "..."
	}
	// Replace FTS5 markers with color escapes.
	s = strings.ReplaceAll(s, ">>>", highlightStyle.Render(""))
	s = strings.ReplaceAll(s, "<<<", lipgloss.NewStyle().Render(""))
	return snippetStyle.Render(s)
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
