// Package transcript renders normalized session entries as human-readable
// terminal text. It is shared by the TUI commands (ccrender, ccbrowse) so a
// session looks the same in every viewer.
package transcript

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/tmc/cc/internal/ccmodel"
)

var (
	dimStyle  = lipgloss.NewStyle().Faint(true)
	userStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	asstStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	toolStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// Options controls rendering.
type Options struct {
	Timestamps bool // prefix each line with the entry's local time
}

// Render formats entries for display in a terminal viewport. Meta entries and
// harness preambles are dropped; tool uses render as one-line summaries and
// tool results as dimmed (or highlighted, on error) excerpts.
func Render(entries []ccmodel.Entry, opts Options) string {
	var b strings.Builder
	for _, e := range entries {
		if e.IsMeta || e.Message == nil {
			continue
		}
		prefix := ""
		if opts.Timestamps && !e.Timestamp.IsZero() {
			prefix = dimStyle.Render(e.Timestamp.Local().Format("15:04:05 "))
		}
		switch e.Message.Role {
		case "user":
			if e.Message.IsToolResultOnly() {
				for _, tr := range e.Message.ToolResults() {
					txt := collapse(tr.Content, 200)
					if txt == "" {
						continue
					}
					style := dimStyle
					if tr.IsError {
						style = toolStyle
					}
					fmt.Fprintf(&b, "%s  %s\n", prefix, style.Render("⎿ "+txt))
				}
				continue
			}
			if txt := e.Message.TextContent(); txt != "" && !ccmodel.IsHarnessPreamble(txt) {
				fmt.Fprintf(&b, "%s%s %s\n\n", prefix, userStyle.Render(">"), txt)
			}
		case "assistant":
			for _, blk := range e.Message.ContentBlocks() {
				switch blk.Type {
				case "text":
					if blk.Text != "" {
						fmt.Fprintf(&b, "%s%s %s\n\n", prefix, asstStyle.Render("⏺"), blk.Text)
					}
				case "tool_use":
					detail := blk.BashCommand()
					if detail == "" {
						detail = collapse(string(blk.Input), 120)
					}
					fmt.Fprintf(&b, "%s%s\n", prefix, toolStyle.Render("⏺ "+blk.Name+" "+detail))
				}
			}
		}
	}
	return b.String()
}

func collapse(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		return s[:max] + "…"
	}
	return s
}
