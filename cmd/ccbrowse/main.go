// Command ccbrowse browses coding-agent sessions in a TUI without resuming them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/transcript"
)

var (
	sinceFlag = flag.String("since", "14d", "Only show sessions modified within duration")
	allFlag   = flag.Bool("all", false, "Show sessions from all projects, not just the current directory's")
	nFlag     = flag.Int("n", 200, "Cap the list to N sessions")
	maxFlag   = flag.Int("max", 2000, "Render at most the last N entries of a transcript (0 = no limit)")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccbrowse: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	since, err := ccpaths.ParseDuration(*sinceFlag)
	if err != nil {
		return fmt.Errorf("parse -since: %w", err)
	}
	// Resolve symlinks so a cwd reached via e.g. ~/go (a symlink) still
	// matches session CWDs recorded under the physical path.
	cwd, _ := os.Getwd()
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	rows := make(chan row)
	go loadRows(since, cwd, rows)

	m := newModel(rows)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr))
	final, err := p.Run()
	if err != nil {
		return err
	}
	// The resume command is the only stdout output, printed after the TUI
	// releases the terminal so it survives in scrollback and pipes cleanly.
	if fm, ok := final.(model); ok && fm.yanked != "" {
		fmt.Println(fm.yanked)
	}
	return nil
}

// row is one session in the list.
type row struct {
	file string
	sum  cc.SessionSummary
	mod  time.Time
}

// loadRows scans for session files and summarizes them with bounded
// concurrency, streaming each finished row to ch. It closes ch when done.
func loadRows(since time.Duration, cwd string, ch chan<- row) {
	defer close(ch)
	ctx := context.Background()
	files, err := cc.FindSessionFiles(ctx, since, "")
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for _, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			info, err := os.Stat(f)
			if err != nil {
				return
			}
			sum, err := cc.SummarizeFile(ctx, f)
			if err != nil {
				return
			}
			if !*allFlag && cwd != "" && !inProject(sum, cwd) {
				return
			}
			ch <- row{file: f, sum: sum, mod: info.ModTime()}
		}()
	}
	wg.Wait()
}

// inProject reports whether the session ran in cwd or a directory below it.
func inProject(sum cc.SessionSummary, cwd string) bool {
	dirs := sum.DistinctCWDs
	if len(dirs) == 0 && sum.CWD != "" {
		dirs = []string{sum.CWD}
	}
	for _, d := range dirs {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			d = r
		}
		if d == cwd || strings.HasPrefix(d, cwd+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// promptTitle cleans a first prompt for display. Antigravity wraps prompts in
// <USER_REQUEST>…</USER_REQUEST> followed by metadata blocks; show only the
// request itself. Claude slash-command turns persist as XML-tagged entries;
// show the bare command.
func promptTitle(s string) string {
	if _, rest, ok := strings.Cut(s, "<USER_REQUEST>"); ok {
		if inner, _, ok := strings.Cut(rest, "</USER_REQUEST>"); ok {
			return strings.TrimSpace(inner)
		}
		return strings.TrimSpace(rest)
	}
	if _, rest, ok := strings.Cut(s, "<command-name>"); ok {
		name, _, _ := strings.Cut(rest, "</command-name>")
		if _, rest, ok := strings.Cut(s, "<command-args>"); ok {
			if args, _, _ := strings.Cut(rest, "</command-args>"); strings.TrimSpace(args) != "" {
				name += " " + strings.TrimSpace(args)
			}
		}
		return strings.TrimSpace(name)
	}
	return s
}

// resumeCommand renders the shell command that would resume the session.
// ccbrowse never runs it; it only prints it on request.
func resumeCommand(r row) string {
	var bin string
	var args []string
	switch cc.Harness(r.file) {
	case "codex":
		bin, args = "codex", []string{"resume", r.sum.SessionID}
	case "gemini":
		bin, args = "gemini", []string{"-r", r.sum.SessionID}
	case "opencode":
		bin, args = "opencode", []string{"--session", r.sum.SessionID}
	case "pi":
		bin, args = "pi", []string{"--session", r.file}
	default:
		bin, args = "claude", []string{"-r", r.sum.SessionID}
	}
	parts := []string{bin}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	if dir := r.sum.CWD; dir != "" {
		return fmt.Sprintf("cd %s; %s", homeQuote(dir), cmd)
	}
	return cmd
}

// homeQuote renders a directory for a cd command, abbreviating a leading
// $HOME to ~ (outside the quotes, so the shell still expands it).
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

// shellQuote wraps s in single quotes when it contains any character the
// shell would interpret.
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
		default:
			safe = false
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- TUI ---

type view int

const (
	listView view = iota
	transcriptView
)

type model struct {
	rows    []row
	ch      <-chan row
	loading bool

	filter   string
	filtered []int // indexes into rows
	cursor   int

	view       view
	viewport   viewport.Model
	transcript row
	timestamps bool
	entries    []cc.Entry

	width, height int
	yanked        string // resume command to print after exit
	err           error
}

type rowMsg struct {
	row row
	ok  bool
}

func newModel(ch <-chan row) model {
	return model{ch: ch, loading: true}
}

func waitRow(ch <-chan row) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		return rowMsg{row: r, ok: ok}
	}
}

func (m model) Init() tea.Cmd {
	return waitRow(m.ch)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case rowMsg:
		if !msg.ok {
			m.loading = false
			return m, nil
		}
		if len(m.rows) < *nFlag {
			m.rows = append(m.rows, msg.row)
			sort.SliceStable(m.rows, func(i, j int) bool {
				return m.rows[i].mod.After(m.rows[j].mod)
			})
			m.refilter()
		}
		return m, waitRow(m.ch)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 2
		return m, nil

	case tea.KeyMsg:
		if m.view == transcriptView {
			return m.updateTranscript(msg)
		}
		return m.updateList(msg)
	}
	return m, nil
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.refilter()
			return m, nil
		}
		return m, tea.Quit
	case "up", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "ctrl+n":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
	case "enter":
		if r, ok := m.selected(); ok {
			return m.openTranscript(r)
		}
	case "y":
		// y is a filter character too; only treat it as yank when the
		// filter is empty, so searching for "y..." still works via paste
		// or by typing other characters first. Ctrl-Y always yanks.
		if m.filter != "" {
			m.filter += "y"
			m.refilter()
			break
		}
		fallthrough
	case "ctrl+y":
		if r, ok := m.selected(); ok {
			m.yanked = resumeCommand(r)
			return m, tea.Quit
		}
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.refilter()
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			m.filter += string(msg.Runes)
			m.refilter()
		}
	}
	return m, nil
}

func (m model) updateTranscript(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "q":
		m.view = listView
		return m, nil
	case "g":
		m.viewport.GotoTop()
		return m, nil
	case "G":
		m.viewport.GotoBottom()
		return m, nil
	case "t":
		m.timestamps = !m.timestamps
		m.viewport.SetContent(renderTranscript(m.transcript.file, m.entries, m.timestamps, m.width))
		return m, nil
	case "y", "ctrl+y":
		m.yanked = resumeCommand(m.transcript)
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m model) selected() (row, bool) {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return row{}, false
	}
	return m.rows[m.filtered[m.cursor]], true
}

func (m model) openTranscript(r row) (tea.Model, tea.Cmd) {
	entries, err := cc.ReadFileWithSubagents(context.Background(), r.file)
	if err != nil {
		m.err = err
		return m, nil
	}
	if *maxFlag > 0 && len(entries) > *maxFlag {
		entries = entries[len(entries)-*maxFlag:]
	}
	m.entries = entries
	m.transcript = r
	m.view = transcriptView
	m.viewport = viewport.New(m.width, m.height-2)
	m.viewport.SetContent(renderTranscript(r.file, entries, m.timestamps, m.width))
	m.viewport.GotoBottom()
	return m, nil
}

func (m *model) refilter() {
	m.filtered = m.filtered[:0]
	q := strings.ToLower(m.filter)
	for i, r := range m.rows {
		if q == "" || rowMatches(r, q) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = len(m.filtered) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func rowMatches(r row, q string) bool {
	for _, s := range []string{
		r.sum.SessionID, r.sum.Project, r.sum.CWD, r.sum.GitBranch,
		r.sum.CustomTitle, r.sum.FirstPrompt, cc.Harness(r.file),
	} {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

func (m model) View() string {
	if m.view == transcriptView {
		header := titleStyle.Render(fmt.Sprintf("%s · %s · %s",
			m.transcript.sum.SessionID, m.transcript.sum.Project, cc.Harness(m.transcript.file)))
		footer := dimStyle.Render("↑/↓ scroll · g/G top/bottom · t timestamps · y print resume cmd · esc back")
		return fmt.Sprintf("%s\n%s\n%s", header, m.viewport.View(), footer)
	}

	var b strings.Builder
	status := fmt.Sprintf("ccbrowse (%d sessions)", len(m.filtered))
	if m.loading {
		status += " · loading…"
	}
	fmt.Fprintf(&b, "%s  %s\n", titleStyle.Render(status), dimStyle.Render("⌕ "+m.filter))
	if m.err != nil {
		fmt.Fprintf(&b, "%s\n", toolStyle.Render(m.err.Error()))
	}

	// Two lines per row; keep the cursor in view.
	visible := max((m.height-3)/2, 1)
	start := 0
	if m.cursor >= visible {
		start = m.cursor - visible + 1
	}
	for i := start; i < len(m.filtered) && i < start+visible; i++ {
		r := m.rows[m.filtered[i]]
		marker := "  "
		if i == m.cursor {
			marker = cursorStyle.Render("❯ ")
		}
		title := r.sum.CustomTitle
		if title == "" {
			title = promptTitle(r.sum.FirstPrompt)
		}
		title = collapse(title, m.width-4)
		parts := []string{age(r.mod), cc.Harness(r.file)}
		if r.sum.Project != "" {
			parts = append(parts, r.sum.Project)
		}
		parts = append(parts,
			fmt.Sprintf("%d msgs", r.sum.UserMessages+r.sum.AsstMessages),
			r.sum.SessionID)
		meta := strings.Join(parts, " · ")
		fmt.Fprintf(&b, "%s%s\n    %s\n", marker, title, dimStyle.Render(collapse(meta, m.width-4)))
	}
	b.WriteString(dimStyle.Render("type to search · enter view · y print resume cmd · esc quit"))
	return b.String()
}

func renderTranscript(file string, entries []cc.Entry, timestamps bool, width int) string {
	return transcript.Render(entries, transcript.Options{
		Timestamps: timestamps,
		Width:      width,
		Style:      transcript.StyleFor(cc.Harness(file)),
	})
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func collapse(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len(s) > max {
		return s[:max] + "…"
	}
	return s
}
