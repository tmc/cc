package cc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/antigravityread"
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

	// Each project directory holds its own sessions-index.json; the transcripts
	// beside it are far more numerous, so read one level rather than walking.
	for _, dir := range dirs {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() {
				continue
			}
			f := filepath.Join(dir, e.Name(), "sessions-index.json")
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
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

	// The other harnesses have no index file: each scan parses transcripts, so
	// run them concurrently.
	scans := []func(time.Time, string) ([]IndexEntry, error){
		openCodeIndexEntries,
		geminiCLIIndexEntries,
		piIndexEntries,
		antigravityIndexEntries,
	}
	out := make([][]IndexEntry, len(scans))
	errs := make([]error, len(scans))
	var wg sync.WaitGroup
	for i, scan := range scans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = scan(cutoff, project)
		}()
	}
	wg.Wait()
	for i := range scans {
		if errs[i] != nil {
			return nil, errs[i]
		}
		all = append(all, out[i]...)
	}
	return all, nil
}

func geminiCLIIndexEntries(cutoff time.Time, project string) ([]IndexEntry, error) {
	home, err := ccpaths.GeminiHome()
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	project = strings.ToLower(project)
	var entries []IndexEntry
	err = walkGeminiCLI(ctx, filepath.Join(home, "tmp"), cutoff, func(path string, mod time.Time) {
		s, err := SummarizeFile(ctx, path)
		if err != nil || s.TotalLines == 0 || !strings.Contains(strings.ToLower(s.CWD), project) {
			return
		}
		if !s.LastTime.IsZero() {
			mod = s.LastTime
			if mod.Before(cutoff) {
				return
			}
		}
		created := s.FirstTime
		if created.IsZero() {
			created = mod
		}
		entries = append(entries, IndexEntry{
			SessionID:    s.SessionID,
			FullPath:     path,
			ProjectPath:  s.CWD,
			FirstPrompt:  s.FirstPrompt,
			MessageCount: s.UserMessages + s.AsstMessages,
			Created:      created.Format(time.RFC3339Nano),
			Modified:     mod.Format(time.RFC3339Nano),
		})
	})
	return entries, err
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

func antigravitySessionRoots() ([]string, error) {
	var roots []string
	appendRoot := func(path string) {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return
		}
		abs, _ := filepath.Abs(path)
		for _, root := range roots {
			rootAbs, _ := filepath.Abs(root)
			if path == root || abs != "" && abs == rootAbs {
				return
			}
		}
		roots = append(roots, path)
	}

	home, err := ccpaths.AntigravityHome()
	if err == nil {
		appendRoot(filepath.Join(home, "brain"))
	}
	if home, err := ccpaths.GeminiHome(); err == nil && home != "" {
		appendRoot(filepath.Join(home, "antigravity-cli", "brain"))
	}
	return roots, err
}

func antigravityIndexEntries(cutoff time.Time, project string) ([]IndexEntry, error) {
	roots, err := antigravitySessionRoots()
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, r := range roots {
		filepath.Walk(r, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || !info.IsDir() {
				return nil
			}
			if !antigravityread.IsSessionPath(path) {
				return nil
			}
			mod := info.ModTime()
			if _, transcript := antigravityread.TranscriptFile(path); transcript != nil {
				mod = transcript.ModTime()
			}
			if !mod.Before(cutoff) {
				paths = append(paths, path)
			}
			return filepath.SkipDir
		})
	}

	// Each session is a separate transcript parse, so summarize them in
	// parallel; the walk above is only a stat pass.
	found := make([]*IndexEntry, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	for i, path := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			found[i] = antigravityIndexEntry(path, cutoff, project)
		}()
	}
	wg.Wait()
	sessionCache.flush()

	var entries []IndexEntry
	for _, e := range found {
		if e != nil {
			entries = append(entries, *e)
		}
	}
	return entries, nil
}

func antigravityIndexEntry(path string, cutoff time.Time, project string) *IndexEntry {
	ctx := context.Background()
	// Parsing a conversation is expensive, so reuse the entry derived from the
	// same transcripts last time. The cache is only consulted for an unfiltered
	// scan: a project query matches against the whole transcript, not just the
	// fields an entry keeps.
	logs := filepath.Join(path, ".system_generated", "logs")
	stamp := ""
	if project == "" {
		stamp = stampFiles(filepath.Join(logs, "transcript.jsonl"), filepath.Join(logs, "transcript_full.jsonl"))
		if e, ok := sessionCache.get(path, stamp); ok {
			if e.ModifiedTime().Before(cutoff) {
				return nil
			}
			return &e
		}
	}
	if !antigravityPathMatchesProject(ctx, path, strings.ToLower(project)) {
		return nil
	}
	parsed, err := ReadFile(ctx, path)
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
	e := IndexEntry{
		SessionID:    s.SessionID,
		FullPath:     path,
		ProjectPath:  s.CWD,
		GitBranch:    s.GitBranch,
		FirstPrompt:  s.FirstPrompt,
		Summary:      s.CustomTitle,
		MessageCount: s.UserMessages + s.AsstMessages,
		Created:      created,
		Modified:     modified,
	}
	sessionCache.put(path, stamp, e)
	return &e
}
