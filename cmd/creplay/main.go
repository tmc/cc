package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Message represents a Claude Code session message
type Message struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content,omitempty"`
	Message *struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message,omitempty"`
	raw string // Store raw JSON for debugging
}

// String returns a formatted representation of the message
func (m Message) String() string {
	role := m.Type
	if m.Message != nil {
		role = m.Message.Role
	}

	var content string
	if m.Message != nil && m.Message.Content != nil {
		content = string(m.Message.Content)
	} else if m.Content != nil {
		content = string(m.Content)
	}

	// Try to extract text from content blocks if it's an array
	if strings.HasPrefix(content, "[") {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &blocks); err == nil {
			var texts []string
			for _, block := range blocks {
				if block.Text != "" {
					texts = append(texts, block.Text)
				}
			}
			if len(texts) > 0 {
				content = strings.Join(texts, "\n")
			}
		}
	}

	// Clean up JSON quotes if it's a simple string
	content = strings.Trim(content, "\"")

	return fmt.Sprintf("[%s] %s", role, content)
}

type keyMap struct {
	quit       key.Binding
	pause      key.Binding
	up         key.Binding
	down       key.Binding
	follow     key.Binding
	raw        key.Binding
	help       key.Binding
	pageUp     key.Binding
	pageDown   key.Binding
	halfPageUp key.Binding
	halfPageDown key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.help, k.quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.up, k.down, k.pageUp, k.pageDown},
		{k.pause, k.follow, k.raw},
		{k.help, k.quit},
	}
}

var keys = keyMap{
	quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	pause: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "pause/resume"),
	),
	up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	pageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	),
	pageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdn", "page down"),
	),
	halfPageUp: key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("ctrl+u", "½ page up"),
	),
	halfPageDown: key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "½ page down"),
	),
	follow: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "toggle follow"),
	),
	raw: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "toggle raw"),
	),
	help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
}

type model struct {
	messages     []Message
	viewport     viewport.Model
	help         help.Model
	showHelp     bool
	showRaw      bool
	paused       bool
	follow       bool
	speed        float64
	file         string
	sessionID    string
	ready        bool
	err          error
	reader       *bufio.Reader
	fileHandle   *os.File
	playbackIdx  int
	lastUpdate   time.Time
}

type tickMsg time.Time
type newMessageMsg Message
type fileEndMsg struct{}

func tickCmd(speed float64) tea.Cmd {
	interval := time.Duration(float64(time.Second) / speed)
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitForNewContent(file string) tea.Cmd {
	return func() tea.Msg {
		// Poll for file changes
		time.Sleep(500 * time.Millisecond)
		return tickMsg(time.Now())
	}
}

func (m model) Init() tea.Cmd {
	if m.follow {
		return tea.Batch(
			tickCmd(m.speed),
			waitForNewContent(m.file),
		)
	}
	return tickCmd(m.speed)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.quit):
			if m.fileHandle != nil {
				m.fileHandle.Close()
			}
			return m, tea.Quit

		case key.Matches(msg, keys.help):
			m.showHelp = !m.showHelp
			return m, nil

		case key.Matches(msg, keys.pause):
			m.paused = !m.paused
			return m, nil

		case key.Matches(msg, keys.follow):
			m.follow = !m.follow
			if m.follow {
				cmds = append(cmds, waitForNewContent(m.file))
			}
			return m, tea.Batch(cmds...)

		case key.Matches(msg, keys.raw):
			m.showRaw = !m.showRaw
			m.viewport.SetContent(m.renderContent())
			return m, nil

		case key.Matches(msg, keys.up):
			m.viewport.LineUp(1)
			return m, nil

		case key.Matches(msg, keys.down):
			m.viewport.LineDown(1)
			return m, nil

		case key.Matches(msg, keys.pageUp):
			m.viewport.ViewUp()
			return m, nil

		case key.Matches(msg, keys.pageDown):
			m.viewport.ViewDown()
			return m, nil

		case key.Matches(msg, keys.halfPageUp):
			m.viewport.HalfViewUp()
			return m, nil

		case key.Matches(msg, keys.halfPageDown):
			m.viewport.HalfViewDown()
			return m, nil
		}

	case tea.WindowSizeMsg:
		headerHeight := 3
		footerHeight := 2
		if m.showHelp {
			footerHeight = 6
		}
		verticalMargins := headerHeight + footerHeight

		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-verticalMargins)
			m.viewport.YPosition = headerHeight
			m.viewport.SetContent(m.renderContent())
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - verticalMargins
		}

	case tickMsg:
		if !m.paused && m.playbackIdx < len(m.messages) {
			// Playback next message
			m.playbackIdx++
			m.viewport.SetContent(m.renderContent())
			m.viewport.GotoBottom()
			cmds = append(cmds, tickCmd(m.speed))
		} else if m.follow {
			// Try to read new messages
			if newMsg, err := m.readNextMessage(); err == nil {
				m.messages = append(m.messages, newMsg)
				m.playbackIdx = len(m.messages)
				m.viewport.SetContent(m.renderContent())
				m.viewport.GotoBottom()
			}
			cmds = append(cmds, waitForNewContent(m.file))
		}
		m.lastUpdate = time.Time(msg)
		return m, tea.Batch(cmds...)
	}

	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if !m.ready {
		return "\n  Initializing..."
	}

	var status string
	if m.paused {
		status = "⏸ PAUSED"
	} else if m.follow {
		status = "▶ FOLLOWING"
	} else if m.playbackIdx >= len(m.messages) {
		status = "■ COMPLETE"
	} else {
		status = "▶ PLAYING"
	}

	mode := ""
	if m.showRaw {
		mode = " [RAW]"
	}

	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("12")).
		Render(fmt.Sprintf("creplay: %s%s | %d/%d messages | Speed: %.1fx",
			status, mode, m.playbackIdx, len(m.messages), m.speed))

	footer := ""
	if m.showHelp {
		footer = m.help.View(keys)
	} else {
		footer = lipgloss.NewStyle().
			Faint(true).
			Render("Press ? for help • q to quit")
	}

	if m.err != nil {
		return fmt.Sprintf("\n  Error: %v\n", m.err)
	}

	return fmt.Sprintf("%s\n%s\n%s", header, m.viewport.View(), footer)
}

func (m model) renderContent() string {
	var lines []string

	displayCount := m.playbackIdx
	if m.follow || m.paused {
		displayCount = len(m.messages)
	}

	for i := 0; i < displayCount && i < len(m.messages); i++ {
		msg := m.messages[i]
		if m.showRaw {
			lines = append(lines, msg.raw)
		} else {
			formatted := msg.String()
			// Style based on message type
			style := lipgloss.NewStyle()
			if msg.Type == "user" || (msg.Message != nil && msg.Message.Role == "user") {
				style = style.Foreground(lipgloss.Color("10")) // Green
			} else if msg.Type == "assistant" || (msg.Message != nil && msg.Message.Role == "assistant") {
				style = style.Foreground(lipgloss.Color("12")) // Blue
			} else {
				style = style.Foreground(lipgloss.Color("11")) // Yellow
			}
			lines = append(lines, style.Render(formatted))
		}
	}

	return strings.Join(lines, "\n")
}

func (m *model) readNextMessage() (Message, error) {
	if m.reader == nil {
		return Message{}, io.EOF
	}

	line, err := m.reader.ReadString('\n')
	if err != nil {
		return Message{}, err
	}

	var msg Message
	msg.raw = strings.TrimSpace(line)
	if err := json.Unmarshal([]byte(msg.raw), &msg); err != nil {
		// Return a message indicating parse error
		return Message{
			Type:    "error",
			Content: json.RawMessage(fmt.Sprintf(`"Parse error: %v"`, err)),
			raw:     msg.raw,
		}, nil
	}

	return msg, nil
}

func loadMessages(file string) ([]Message, *bufio.Reader, *os.File, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open file: %w", err)
	}

	reader := bufio.NewReader(f)
	var messages []Message

	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return nil, nil, nil, fmt.Errorf("failed to read file: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var msg Message
		msg.raw = line
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			// Include parse errors as error messages
			msg = Message{
				Type:    "error",
				Content: json.RawMessage(fmt.Sprintf(`"Parse error: %v"`, err)),
				raw:     line,
			}
		}
		messages = append(messages, msg)
	}

	// Reset reader for follow mode
	f.Seek(0, 0)
	reader = bufio.NewReader(f)

	return messages, reader, f, nil
}

func findSessionFile(sessionID string) (string, error) {
	// Try common patterns
	patterns := []string{
		fmt.Sprintf("session-%s.ndjson", sessionID),
		fmt.Sprintf("%s.ndjson", sessionID),
		fmt.Sprintf("*%s*.ndjson", sessionID),
	}

	// Search in current directory and common locations
	searchDirs := []string{
		".",
		".sessions",
		filepath.Join(os.Getenv("HOME"), ".cc", "sessions"),
	}

	for _, dir := range searchDirs {
		for _, pattern := range patterns {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				continue
			}
			if len(matches) > 0 {
				return matches[0], nil
			}
		}
	}

	return "", fmt.Errorf("session file not found for ID: %s", sessionID)
}

func main() {
	var (
		fileFlag  = flag.String("file", "", "Read session from file")
		follow    = flag.Bool("follow", false, "Follow mode (like tail -f)")
		speed     = flag.Float64("speed", 1.0, "Playback speed multiplier")
	)
	flag.Parse()

	var file string
	var sessionID string

	if *fileFlag != "" {
		file = *fileFlag
	} else if flag.NArg() > 0 {
		sessionID = flag.Arg(0)
		var err error
		file, err = findSessionFile(sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			fmt.Fprintf(os.Stderr, "Usage: creplay SESSION_ID [options]\n")
			fmt.Fprintf(os.Stderr, "   or: creplay -file FILE [options]\n")
			os.Exit(1)
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: SESSION_ID or -file required\n")
		fmt.Fprintf(os.Stderr, "Usage: creplay SESSION_ID [options]\n")
		fmt.Fprintf(os.Stderr, "   or: creplay -file FILE [options]\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	messages, reader, fileHandle, err := loadMessages(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading messages: %v\n", err)
		os.Exit(1)
	}

	m := model{
		messages:   messages,
		help:       help.New(),
		showHelp:   false,
		showRaw:    false,
		paused:     false,
		follow:     *follow,
		speed:      *speed,
		file:       file,
		sessionID:  sessionID,
		reader:     reader,
		fileHandle: fileHandle,
		playbackIdx: 0,
	}

	// If not following, start with all messages visible
	if !*follow {
		m.playbackIdx = 0 // Will animate through
	} else {
		m.playbackIdx = len(messages) // Show all immediately, wait for new
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if fileHandle != nil {
		fileHandle.Close()
	}
}
