package cc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/opencoderead"
	"github.com/tmc/cc/internal/piread"
)

// SessionIndex is the sessions-index.json file written by Claude Code.
type SessionIndex struct {
	Version      int          `json:"version"`
	OriginalPath string       `json:"originalPath"`
	Entries      []IndexEntry `json:"entries"`
}

// IndexEntry is a single session in the index.
type IndexEntry struct {
	SessionID    string  `json:"sessionId"`
	FullPath     string  `json:"fullPath"`
	ProjectPath  string  `json:"projectPath"`
	GitBranch    string  `json:"gitBranch,omitempty"`
	FirstPrompt  string  `json:"firstPrompt,omitempty"`
	Summary      string  `json:"summary,omitempty"`
	MessageCount int     `json:"messageCount"`
	IsSidechain  bool    `json:"isSidechain,omitempty"`
	Created      string  `json:"created"`
	Modified     string  `json:"modified"`
	FileMtime    float64 `json:"fileMtime"`
}

// CreatedTime parses the Created timestamp.
func (e IndexEntry) CreatedTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Created)
	return t
}

// ModifiedTime parses the Modified timestamp.
func (e IndexEntry) ModifiedTime() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Modified)
	return t
}

// ReadIndex reads a sessions-index.json file.
func ReadIndex(path string) (*SessionIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var idx SessionIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// FindIndexFiles finds all sessions-index.json files under ~/.claude/projects/ and ~/.gemini/projects/.
func FindIndexFiles() ([]string, error) {
	ch, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, err
	}
	gh, _ := ccpaths.GeminiHome()

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
			if info.Name() == "sessions-index.json" {
				files = append(files, path)
			}
			return nil
		})
	}
	return files, nil
}

// AllIndexEntries reads all index files and returns entries, optionally
// filtered by since duration and project substring.
func AllIndexEntries(since time.Duration, project string) ([]IndexEntry, error) {
	files, err := FindIndexFiles()
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-since)
	var all []IndexEntry
	for _, f := range files {
		idx, err := ReadIndex(f)
		if err != nil {
			continue
		}
		for _, e := range idx.Entries {
			if e.ModifiedTime().Before(cutoff) {
				continue
			}
			if project != "" && !strings.Contains(strings.ToLower(e.ProjectPath), strings.ToLower(project)) {
				continue
			}
			all = append(all, e)
		}
	}
	openCodeEntries, err := openCodeIndexEntries(cutoff, project)
	if err != nil {
		return nil, err
	}
	all = append(all, openCodeEntries...)

	piEntries, err := piIndexEntries(cutoff, project)
	if err != nil {
		return nil, err
	}
	all = append(all, piEntries...)
	return all, nil
}

func piIndexEntries(cutoff time.Time, project string) ([]IndexEntry, error) {
	home, err := ccpaths.PiHome()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "sessions")
	var entries []IndexEntry
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !piread.IsSessionPath(path) || info.ModTime().Before(cutoff) {
			return nil
		}
		if !piPathMatchesProject(context.Background(), path, strings.ToLower(project)) {
			return nil
		}
		parsed, err := ReadFile(context.Background(), path)
		if err != nil {
			return nil
		}
		s := Summarize(path, parsed)
		if s.TotalLines == 0 || s.LastTime.Before(cutoff) {
			return nil
		}
		modified := s.LastTime.Format(time.RFC3339Nano)
		created := modified
		if !s.FirstTime.IsZero() {
			created = s.FirstTime.Format(time.RFC3339Nano)
		}
		entries = append(entries, IndexEntry{
			SessionID:    s.SessionID,
			FullPath:     path,
			ProjectPath:  s.CWD,
			GitBranch:    s.GitBranch,
			FirstPrompt:  s.FirstPrompt,
			Summary:      s.CustomTitle,
			MessageCount: s.UserMessages + s.AsstMessages,
			Created:      created,
			Modified:     modified,
		})
		return nil
	})
	if os.IsNotExist(err) {
		return entries, nil
	}
	return entries, nil
}

func openCodeIndexEntries(cutoff time.Time, project string) ([]IndexEntry, error) {
	home, err := ccpaths.OpenCodeHome()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(home, "storage", "session")
	var entries []IndexEntry
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !opencoderead.IsSessionPath(path) || info.ModTime().Before(cutoff) {
			return nil
		}
		if !openCodePathMatchesProject(context.Background(), path, project) {
			return nil
		}
		parsed, err := ReadFile(context.Background(), path)
		if err != nil {
			return nil
		}
		s := Summarize(path, parsed)
		if s.TotalLines == 0 || s.LastTime.Before(cutoff) {
			return nil
		}
		modified := s.LastTime.Format(time.RFC3339Nano)
		created := modified
		if !s.FirstTime.IsZero() {
			created = s.FirstTime.Format(time.RFC3339Nano)
		}
		entries = append(entries, IndexEntry{
			SessionID:    s.SessionID,
			FullPath:     path,
			ProjectPath:  s.CWD,
			GitBranch:    s.GitBranch,
			FirstPrompt:  s.FirstPrompt,
			Summary:      s.CustomTitle,
			MessageCount: s.UserMessages + s.AsstMessages,
			Created:      created,
			Modified:     modified,
		})
		return nil
	})
	if os.IsNotExist(err) {
		return entries, nil
	}
	return entries, nil
}
