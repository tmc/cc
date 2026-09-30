package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tmc/cc/ccfs"
	"github.com/tmc/cc/ccgit"
)

// Building an index entry for a harness without its own index file means
// parsing the session transcript: an antigravity brain holding a week of
// conversations is on the order of a gigabyte, which no interactive command
// can afford to re-read on every run. entryCache memoizes those entries on
// disk, keyed by session path and validated against a stamp of the files the
// entry was derived from, so a session is parsed once and then only again when
// its transcript changes.
type entryCache struct {
	mu        sync.Mutex
	entries   map[string]cachedEntry
	summaries map[string]cachedSummary
	dirty     bool
	loaded    bool
}

type cachedEntry struct {
	Stamp string     `json:"stamp"`
	Entry IndexEntry `json:"entry"`
}

type cachedSummary struct {
	Stamp   string         `json:"stamp"`
	Summary SessionSummary `json:"summary"`
}

// cacheFormat is the on-disk layout; bump the file name to invalidate.
type cacheFormat struct {
	Entries   map[string]cachedEntry   `json:"entries"`
	Summaries map[string]cachedSummary `json:"summaries,omitempty"`
}

var sessionCache entryCache

func cacheFile() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cc", "sessionentries-v1.json"), nil
}

// stampFiles renders the size and modification time of paths as a validity
// stamp. Missing files contribute nothing, so a stamp stays stable as long as
// the files the entry was built from do.
func stampFiles(paths ...string) string {
	s := ""
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		s += fmt.Sprintf("%s:%d:%d;", filepath.Base(p), info.Size(), info.ModTime().UnixNano())
	}
	return s
}

// get returns the entry cached for key, if it was built from the same files.
func (c *entryCache) get(key, stamp string) (IndexEntry, bool) {
	if stamp == "" {
		return IndexEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	e, ok := c.entries[key]
	if !ok || e.Stamp != stamp {
		return IndexEntry{}, false
	}
	return e.Entry, true
}

// put records entry under key. Nothing is written until flush.
func (c *entryCache) put(key, stamp string, entry IndexEntry) {
	if stamp == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.entries[key] = cachedEntry{Stamp: stamp, Entry: entry}
	c.dirty = true
}

// load reads the cache file once. c.mu must be held.
func (c *entryCache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	c.entries = map[string]cachedEntry{}
	c.summaries = map[string]cachedSummary{}
	path, err := cacheFile()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f cacheFormat
	if json.Unmarshal(data, &f) != nil || f.Entries == nil {
		return
	}
	c.entries = f.Entries
	if f.Summaries != nil {
		c.summaries = f.Summaries
	}
}

// flush writes the cache back if it changed. A stale or unwritable cache only
// costs a reparse, so errors are dropped. Entries for sessions no longer seen
// are kept: they are small, and a session outside one scan's window can be
// inside the next one's.
func (c *entryCache) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	c.dirty = false
	c.prune()
	path, err := cacheFile()
	if err != nil {
		return
	}
	data, err := json.Marshal(cacheFormat{Entries: c.entries, Summaries: c.summaries})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	ccfs.WriteFileAtomic(path, data, 0o644)
}

// prune drops records for sessions that no longer exist, keeping the file from
// growing without bound. Sessions are cheap to record and expensive to rebuild,
// so this only runs once the cache is large. c.mu must be held.
func (c *entryCache) prune() {
	const maxRecords = 10000
	if len(c.entries)+len(c.summaries) <= maxRecords {
		return
	}
	for k := range c.entries {
		if _, err := os.Stat(k); err != nil {
			delete(c.entries, k)
		}
	}
	for k := range c.summaries {
		if _, err := os.Stat(k); err != nil {
			delete(c.summaries, k)
		}
	}
}

// sessionStamp renders the validity stamp for the session at path: the file
// itself, or, for a session held in a directory, the transcripts it is read
// from.
func sessionStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if !info.IsDir() {
		return stampFiles(path)
	}
	logs := filepath.Join(path, ".system_generated", "logs")
	return stampFiles(filepath.Join(logs, "transcript.jsonl"), filepath.Join(logs, "transcript_full.jsonl"))
}

// SummarizeFileCached is [SummarizeFile] backed by an on-disk cache of
// summaries, keyed by path and invalidated when the session's size or
// modification time changes. Summarizing a large transcript costs a full parse,
// which an interactive command re-runs on every invocation; a session that has
// not changed is summarized once.
func SummarizeFileCached(ctx context.Context, path string) (SessionSummary, error) {
	stamp := sessionStamp(path)
	if s, ok := sessionCache.getSummary(path, stamp); ok {
		// Older cache records may contain implicitly resolved git metadata.
		s.GitContext = ccgit.GitContext{}
		return s, nil
	}
	s, err := SummarizeFile(ctx, path)
	if err != nil {
		return s, err
	}
	sessionCache.putSummary(path, stamp, s)
	sessionCache.flush()
	return s, nil
}

func (c *entryCache) getSummary(key, stamp string) (SessionSummary, bool) {
	if stamp == "" {
		return SessionSummary{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	s, ok := c.summaries[key]
	if !ok || s.Stamp != stamp {
		return SessionSummary{}, false
	}
	return s.Summary, true
}

func (c *entryCache) putSummary(key, stamp string, s SessionSummary) {
	if stamp == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.summaries[key] = cachedSummary{Stamp: stamp, Summary: s}
	c.dirty = true
}
