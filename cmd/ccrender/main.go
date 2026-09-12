// Command ccrender renders a single coding-agent session in the pager.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/sessionview"
	"github.com/tmc/cc/internal/transcript"
)

var (
	formatFlag = flag.String("format", "", "Delegate to ccfmt with this format instead of the TUI")
	followFlag = flag.Bool("follow", false, "Tail the session file for new entries (TUI only)")
	tuiFlag    = flag.Bool("tui", false, "Use the built-in viewport instead of the pager")
	tsFlag     = flag.Bool("timestamps", false, "Start with per-entry timestamps shown")
	expandFlag = flag.Bool("expand", false, "Start with folded output (tool results, Explored groups) expanded")
	maxFlag    = flag.Int("max", 2000, "Render at most the last N entries (0 = no limit)")
	sinceFlag  = flag.String("since", "30d", "How far back to search when resolving an ID")
	searchFlag = flag.String("search", "", "Open the pager at the first match of this pattern")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccrender: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if flag.NArg() != 1 {
		return fmt.Errorf("usage: ccrender [flags] SESSION_ID_OR_PATH")
	}
	file, err := resolve(flag.Arg(0))
	if err != nil {
		return err
	}

	// On a terminal the transcript goes through the pager, which already has
	// the navigation a reader wants (search, marks, line jumps). The viewport
	// owns what a pager cannot do: follow a live session. A piped stdout gets
	// the same text with no pager at all, so the two stay in lockstep.
	opts := sessionview.Options{
		Timestamps: *tsFlag,
		Expand:     *expandFlag,
		Max:        *maxFlag,
		Search:     *searchFlag,
	}
	format := *formatFlag
	if format == "" {
		if *followFlag || *tuiFlag {
			return runTUI(file)
		}
		return sessionview.Page(context.Background(), file, opts)
	}
	// Batch formats belong to ccfmt.
	cmd := exec.Command("ccfmt", "-format", format, file)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// A closed downstream pipe (ccrender ... | head) is normal
		// termination, not a rendering failure.
		if strings.Contains(err.Error(), "broken pipe") {
			return nil
		}
		return fmt.Errorf("ccfmt: %w", err)
	}
	return nil
}

func runTUI(file string) error {
	m, err := newModel(file)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// resolve maps arg to a session file: an existing path is used as-is, and
// anything else is treated as a session ID (or ID prefix) to look up, first
// in the fast index and then by scanning session filenames.
func resolve(arg string) (string, error) {
	since, err := ccpaths.ParseDuration(*sinceFlag)
	if err != nil {
		return "", fmt.Errorf("parse -since: %w", err)
	}
	return cc.ResolveSessionFile(context.Background(), arg, since)
}

// --- TUI ---

var (
	titleStyle = lipgloss.NewStyle().Bold(true)
	dimStyle   = lipgloss.NewStyle().Faint(true)
)

type model struct {
	file       string
	entries    []cc.Entry
	offset     int64
	tailable   bool // offset-based tailing works (raw claude/codex JSONL)
	size       int64
	viewport   viewport.Model
	timestamps bool
	expand     bool
	follow     bool
	width      int
	height     int
	ready      bool
}

type tickMsg struct{}

func newModel(file string) (model, error) {
	m := model{
		file:       file,
		timestamps: *tsFlag,
		expand:     *expandFlag,
		follow:     *followFlag,
	}
	// Offset tailing reads raw claude, codex, and pi JSONL lines. Every initial render
	// includes sidechain entries, the same session view ccfmt uses.
	switch cc.Harness(file) {
	case "claude", "codex", "pi":
		entries, offset, _, err := cc.ReadFileWithOffset(context.Background(), file)
		if err != nil {
			return model{}, err
		}
		if cc.Harness(file) != "pi" {
			subs, err := cc.ReadSubagents(context.Background(), file)
			if err != nil {
				return model{}, err
			}
			entries = append(entries, subs...)
			sort.SliceStable(entries, func(i, j int) bool {
				return entries[i].Timestamp.Before(entries[j].Timestamp)
			})
		}
		m.entries, m.offset, m.tailable = entries, offset, true
	default:
		entries, err := cc.ReadFileWithSubagents(context.Background(), file)
		if err != nil {
			return model{}, err
		}
		m.entries = entries
		if fi, err := os.Stat(file); err == nil {
			m.size = fi.Size()
		}
	}
	return m, nil
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m model) Init() tea.Cmd {
	if m.follow {
		return tick()
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-2)
			m.viewport.SetContent(m.render())
			m.viewport.GotoBottom()
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - 2
			m.viewport.SetContent(m.render())
		}
		return m, nil

	case tickMsg:
		if !m.follow {
			return m, nil
		}
		if m.tailable {
			entries, newOffset, _, err := cc.ReadFileFrom(context.Background(), m.file, m.offset)
			if err == nil && len(entries) > 0 {
				atBottom := m.viewport.AtBottom()
				m.entries = append(m.entries, entries...)
				m.offset = newOffset
				m.viewport.SetContent(m.render())
				if atBottom {
					m.viewport.GotoBottom()
				}
			} else if err == nil {
				m.offset = newOffset
			}
			return m, tick()
		}
		if fi, err := os.Stat(m.file); err == nil && fi.Size() != m.size {
			if entries, err := cc.ReadFile(context.Background(), m.file); err == nil {
				atBottom := m.viewport.AtBottom()
				m.entries = entries
				m.size = fi.Size()
				m.viewport.SetContent(m.render())
				if atBottom {
					m.viewport.GotoBottom()
				}
			}
		}
		return m, tick()

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "g":
			m.viewport.GotoTop()
			return m, nil
		case "G":
			m.viewport.GotoBottom()
			return m, nil
		case "t":
			m.timestamps = !m.timestamps
			m.viewport.SetContent(m.render())
			return m, nil
		case "ctrl+o":
			// ctrl+o expands folded output, matching the native TUIs.
			m.expand = !m.expand
			atBottom := m.viewport.AtBottom()
			m.viewport.SetContent(m.render())
			if atBottom {
				m.viewport.GotoBottom()
			}
			return m, nil
		case "f":
			m.follow = !m.follow
			if m.follow {
				return m, tick()
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m model) render() string {
	entries := m.entries
	if *maxFlag > 0 && len(entries) > *maxFlag {
		entries = entries[len(entries)-*maxFlag:]
	}
	return transcript.Render(entries, transcript.Options{
		Timestamps: m.timestamps,
		Expand:     m.expand,
		Width:      m.width,
		Style:      transcript.StyleFor(cc.Harness(m.file)),
	})
}

func (m model) View() string {
	if !m.ready {
		return "loading…"
	}
	mode := ""
	if m.follow {
		mode = " · following"
	}
	header := titleStyle.Render(fmt.Sprintf("%s · %d entries%s", m.file, len(m.entries), mode))
	footer := dimStyle.Render("↑/↓ scroll · g/G top/bottom · ctrl+o expand · t timestamps · f follow · q quit")
	return fmt.Sprintf("%s\n%s\n%s", header, m.viewport.View(), footer)
}
