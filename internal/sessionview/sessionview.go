// Package sessionview shows one session transcript the way a reader wants it:
// rendered to terminal text and handed to the user's pager.
//
// ccrender owns the view; cass and any other command that wants to show a
// session calls it here rather than running ccrender as a subprocess, which
// needs ccrender on $PATH to do work that is already linked in.
package sessionview

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
	"github.com/tmc/cc"
	"github.com/tmc/cc/internal/transcript"
)

// Options control how a session is rendered and paged. The zero value renders
// every entry at the terminal's width with no timestamps.
type Options struct {
	Timestamps bool   // show a timestamp on each entry
	Expand     bool   // expand tool results and Explored groups
	Max        int    // render at most the last Max entries; 0 means all
	Width      int    // wrap width; 0 detects the terminal's
	Search     string // pager pattern to open at (a less regexp)
}

// Render writes file's transcript to w.
func Render(ctx context.Context, w io.Writer, file string, opts Options) error {
	entries, err := cc.ReadFileWithSubagents(ctx, file)
	if err != nil {
		return err
	}
	if opts.Max > 0 && len(entries) > opts.Max {
		entries = entries[len(entries)-opts.Max:]
	}
	width := opts.Width
	if width <= 0 {
		width = TermWidth()
	}
	_, err = io.WriteString(w, transcript.Render(entries, transcript.Options{
		Timestamps: opts.Timestamps,
		Expand:     opts.Expand,
		Width:      width,
		Style:      transcript.StyleFor(cc.Harness(file)),
	}))
	return err
}

// Page renders file and shows it in the user's pager, opening at Options.Search
// when one is given. A stdout that is not a terminal gets the same text
// directly, so the paged and piped views stay in lockstep.
func Page(ctx context.Context, file string, opts Options) error {
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return Render(ctx, os.Stdout, file, opts)
	}
	var b strings.Builder
	if err := Render(ctx, &b, file, opts); err != nil {
		return err
	}
	return runPager(b.String(), opts.Search)
}

// TermWidth reports the width to wrap at: the terminal's, then $COLUMNS, then
// a terminal-typical 100.
func TermWidth() int {
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		return w
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 100
}
