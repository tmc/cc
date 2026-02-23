package cc

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Reader reads entries from a JSONL session file.
type Reader struct {
	scanner *bufio.Scanner
	err     error
	entry   Entry
}

// NewReader creates a Reader from an io.Reader.
func NewReader(r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 256*1024), 10*1024*1024)
	return &Reader{scanner: s}
}

// Next advances to the next entry. Returns false at EOF or on error.
func (r *Reader) Next() bool {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			continue
		}
		r.entry = Entry{}
		if err := json.Unmarshal([]byte(line), &r.entry); err != nil {
			continue
		}
		return true
	}
	r.err = r.scanner.Err()
	return false
}

// Entry returns the current entry.
func (r *Reader) Entry() Entry { return r.entry }

// Err returns any error from scanning.
func (r *Reader) Err() error { return r.err }

// ReadAll reads all entries from the reader.
func ReadAll(r io.Reader) ([]Entry, error) {
	rd := NewReader(r)
	var entries []Entry
	for rd.Next() {
		entries = append(entries, rd.Entry())
	}
	return entries, rd.Err()
}

// ReadFile reads all entries from a JSONL file.
func ReadFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAll(f)
}

// SessionSummary holds summarized metadata for a session file.
type SessionSummary struct {
	SessionID    string    `json:"session_id"`
	File         string    `json:"file"`
	Project      string    `json:"project"`
	CWD          string    `json:"cwd,omitempty"`
	GitBranch    string    `json:"git_branch,omitempty"`
	Version      string    `json:"version,omitempty"`
	Slug         string    `json:"slug,omitempty"`
	Model        string    `json:"model,omitempty"`
	FirstTime    time.Time `json:"first_time"`
	LastTime     time.Time `json:"last_time"`
	UserMessages int       `json:"user_messages"`
	AsstMessages int       `json:"asst_messages"`
	ToolUses     int       `json:"tool_uses"`
	TotalLines   int       `json:"total_lines"`
	Compactions  int       `json:"compactions,omitempty"`
	FirstPrompt  string    `json:"first_prompt,omitempty"`
	CustomTitle  string    `json:"custom_title,omitempty"`
}

// Summarize builds a SessionSummary from entries.
func Summarize(file string, entries []Entry) SessionSummary {
	s := SessionSummary{File: file}
	for _, e := range entries {
		s.TotalLines++
		if e.SessionID != "" && s.SessionID == "" {
			s.SessionID = e.SessionID
		}
		if e.Version != "" && s.Version == "" {
			s.Version = e.Version
		}
		if e.CWD != "" && s.CWD == "" {
			s.CWD = e.CWD
		}
		if e.GitBranch != "" && s.GitBranch == "" {
			s.GitBranch = e.GitBranch
		}
		if e.Slug != "" && s.Slug == "" {
			s.Slug = e.Slug
		}
		if !e.Timestamp.IsZero() {
			if s.FirstTime.IsZero() {
				s.FirstTime = e.Timestamp
			}
			s.LastTime = e.Timestamp
		}
		if e.Type == "custom-title" && e.CustomTitle != "" {
			s.CustomTitle = e.CustomTitle
		}
		if e.Type == "system" && e.Subtype == "compact_boundary" {
			s.Compactions++
		}
		if e.Message != nil && !e.IsCompactSummary {
			switch e.Message.Role {
			case "user":
				s.UserMessages++
				if s.FirstPrompt == "" {
					s.FirstPrompt = ExtractText(e.Message.Content)
				}
				if s.Model == "" && e.Message.Model != "" {
					s.Model = e.Message.Model
				}
			case "assistant":
				s.AsstMessages++
				if s.Model == "" && e.Message.Model != "" {
					s.Model = e.Message.Model
				}
				// Count tool uses.
				var blocks []ContentBlock
				if json.Unmarshal(e.Message.Content, &blocks) == nil {
					for _, b := range blocks {
						if b.Type == "tool_use" {
							s.ToolUses++
						}
					}
				}
			}
		}
	}
	return s
}

// ExtractText pulls the first text content from a message content field.
func ExtractText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return collapseWhitespace(s, 200)
	}
	var blocks []ContentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return collapseWhitespace(b.Text, 200)
			}
		}
	}
	return ""
}

func collapseWhitespace(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// FindSessionFiles finds JSONL session files under ~/.claude/projects/ and ~/.gemini/projects/.
// It excludes subagent files and filters by modification time.
func FindSessionFiles(since time.Duration, project string) ([]string, error) {
	ch, err := ClaudeHome()
	if err != nil {
		return nil, err
	}
	gh, _ := GeminiHome()

	cutoff := time.Now().Add(-since)
	var files []string

	dirs := []string{filepath.Join(ch, "projects")}
	if gh != "" {
		dirs = append(dirs, filepath.Join(gh, "projects"))
	}

	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() && info.Name() == "subagents" {
				return filepath.SkipDir
			}
			if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				return nil
			}
			if project != "" {
				rel, _ := filepath.Rel(dir, path)
				if !strings.Contains(strings.ToLower(rel), strings.ToLower(project)) {
					return nil
				}
			}
			files = append(files, path)
			return nil
		})
	}
	return files, nil
}
