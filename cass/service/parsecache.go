package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tmc/cc"
)

// ParseCache speeds up repeated re-indexing of growing session files. Keyed by
// canonical file path, it remembers the parsed entries of each file along with
// the source size, mtime, and the byte offset just past the last complete line.
// On the next read it does one of three things:
//
//   - skip: source size and mtime are unchanged, so the cached entries are
//     returned with no read or parse (this is the "mtime/size short-circuit");
//   - tail: the file strictly grew and mtime moved forward, so only the
//     appended bytes are read and decoded via [cc.ReadFileFrom] and appended to
//     the cached prefix;
//   - full: anything ambiguous (shrink, backward mtime, non-boundary offset,
//     cache miss/eviction, or a stat that changes mid-read) falls back to a
//     full [cc.ReadFileWithOffset].
//
// ParseFile always returns the same entries a plain [cc.ReadFile] would for the
// current file contents; the cache only avoids re-reading unchanged bytes. The
// zero value is not usable; use [NewParseCache]. A nil *ParseCache is a valid
// no-op: ParseFile on it reads in full every time, so callers that do not want
// caching can pass nil.
type ParseCache struct {
	maxBytes int64 // total cached entry-byte budget; <=0 disables caching.

	mu        sync.Mutex
	entries   map[string]*cacheEntry
	totalSize int64
	clock     int64 // monotonic tick for LRU.
}

type cacheEntry struct {
	size     int64
	mtime    time.Time
	offset   int64      // byte just past the last complete line of the cached entries.
	anchor   []byte     // bytes ending at offset; re-checked before tailing.
	entries  []cc.Entry // complete (newline-terminated) lines only.
	partial  *cc.Entry  // decoded unterminated trailing line, if any; excluded from offset/entries.
	bytes    int64      // estimated heap cost, for the LRU budget.
	lastUsed int64
}

// anchorLen is how many bytes ending at the cached offset we fingerprint. A
// pure append leaves these bytes untouched; a rewrite (even one that grows past
// the old size and happens to leave a newline at offset-1) changes them, so
// re-reading and comparing them rejects a stale-prefix tail. O(anchorLen), not
// O(prefix).
const anchorLen = 256

// NewParseCache returns a cache bounded to roughly maxBytes of cached entry
// data. A maxBytes <= 0 disables caching: ParseFile reads every file in full,
// which is the correct cold behavior for one-shot CLI scans.
func NewParseCache(maxBytes int64) *ParseCache {
	return &ParseCache{
		maxBytes: maxBytes,
		entries:  make(map[string]*cacheEntry),
	}
}

// canonPath resolves symlinks and makes the path absolute so one file maps to
// exactly one cache key (safety rule I). On error it falls back to the input
// path; a wrong key only costs a cache miss, never correctness.
func canonPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// estimateBytes approximates the heap cost of a slice of entries for the LRU
// budget. It is intentionally rough; if memory pressure is ever observed the
// accounting can switch to summing message content lengths.
func estimateBytes(entries []cc.Entry) int64 {
	const perEntry = 512 // struct + typical small message overhead.
	var n int64
	for i := range entries {
		n += perEntry
		if entries[i].Message != nil {
			n += int64(len(entries[i].Message.TextContent()))
		}
	}
	return n
}

// ParseFile reads the entries of the file at path, reusing cached work when the
// file is unchanged (skip) or has only grown (tail). A nil cache reads in full.
func (c *ParseCache) ParseFile(ctx context.Context, path string) ([]cc.Entry, error) {
	if c == nil || c.maxBytes <= 0 {
		return cc.ReadFile(ctx, path)
	}

	key := canonPath(path)
	fi, statErr := os.Stat(path)
	if statErr != nil {
		// Cannot stat: drop any cache entry and surface the error (the caller,
		// e.g. an empty/deleted file, treats it as a skip/full as today).
		c.evict(key)
		return nil, statErr
	}
	size, mtime := fi.Size(), fi.ModTime()

	c.mu.Lock()
	prev, ok := c.entries[key]
	c.mu.Unlock()

	// Skip: exact size+mtime match means the bytes are identical (rule A). The
	// cached entries are complete-only, so re-append the remembered trailing
	// partial to match what cc.ReadFile would return for these bytes.
	if ok && prev.size == size && prev.mtime.Equal(mtime) {
		c.touch(key)
		return withPartial(prev.entries, prev.partial), nil
	}

	// Tail: strict grow with mtime not going backward, and a verified-unchanged
	// prefix. ReadFileFrom enforces the newline-boundary and truncation checks;
	// the anchor re-check rejects a rewrite that grew past the old size but
	// replaced the prefix bytes (a larger rewrite that coincidentally leaves a
	// newline at offset-1).
	if ok && size > prev.size && !mtime.Before(prev.mtime) && c.anchorMatches(path, prev) {
		tail, newOffset, partial, err := cc.ReadFileFrom(ctx, path, prev.offset)
		if err == nil {
			anchor := readAnchorAt(path, newOffset)
			// TOCTOU guard (rule E): only commit if the file did not change
			// between our stat and the read we just performed.
			if fi2, err2 := os.Stat(path); err2 == nil && fi2.Size() == size && fi2.ModTime().Equal(mtime) {
				merged := make([]cc.Entry, 0, len(prev.entries)+len(tail))
				merged = append(merged, prev.entries...)
				merged = append(merged, tail...)
				// Store complete lines only (on the newOffset boundary); the
				// trailing partial must not be cached or it double-counts when
				// the line later completes. It is appended to the return only.
				c.store(key, size, mtime, newOffset, anchor, merged, partial)
				return withPartial(merged, partial), nil
			}
			// File moved under us: fall through to a full read of current bytes.
		} else if !errors.Is(err, cc.ErrTailInvalid) {
			return nil, err
		}
		// ErrTailInvalid or stat drift: fall through to full reparse.
	}

	// Full: any miss, shrink, backward mtime, or tail rejection.
	entries, offset, partial, err := cc.ReadFileWithOffset(ctx, path)
	if err != nil {
		return nil, err
	}
	anchor := readAnchorAt(path, offset)
	// Re-stat after the read so the cached (size,mtime,offset) match the bytes
	// we actually parsed (rule E). If it drifted, return the entries we read
	// but do not cache a stale snapshot.
	if fi2, err2 := os.Stat(path); err2 == nil && fi2.Size() == size && fi2.ModTime().Equal(mtime) {
		c.store(key, size, mtime, offset, anchor, entries, partial)
	} else {
		c.evict(key)
	}
	return withPartial(entries, partial), nil
}

// withPartial returns an independent copy of entries with the unterminated
// trailing line appended, matching what a plain cc.ReadFile yields for the
// current bytes. The partial is never folded into the cached (offset-aligned)
// entries — only into this returned slice — so a later read that finds the line
// completed decodes it exactly once.
func withPartial(entries []cc.Entry, partial *cc.Entry) []cc.Entry {
	out := cloneEntries(entries)
	if partial != nil {
		out = append(out, *partial)
	}
	return out
}

// readAnchorAt reads up to anchorLen bytes ending at off, the fingerprint of
// the last cached line used to detect a prefix rewrite before tailing. Returns
// nil on error or off==0 (an empty prefix has nothing to anchor; a grow from
// zero is always safe to tail).
func readAnchorAt(path string, off int64) []byte {
	if off <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	n := min(int64(anchorLen), off)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off-n); err != nil {
		return nil
	}
	return buf
}

// anchorMatches reports whether the bytes ending at prev.offset still equal the
// cached anchor, i.e. the prefix the cached entries were parsed from is intact.
// A nil cached anchor (prefix grew from empty) always matches.
func (c *ParseCache) anchorMatches(path string, prev *cacheEntry) bool {
	if len(prev.anchor) == 0 {
		return true
	}
	cur := readAnchorAt(path, prev.offset)
	if len(cur) != len(prev.anchor) {
		return false
	}
	for i := range cur {
		if cur[i] != prev.anchor[i] {
			return false
		}
	}
	return true
}

// cloneEntries returns an independent copy of src, normalizing an empty result
// to nil so it matches cc.ReadFile (which returns a nil slice for an empty
// file) exactly under reflect.DeepEqual.
func cloneEntries(src []cc.Entry) []cc.Entry {
	if len(src) == 0 {
		return nil
	}
	out := make([]cc.Entry, len(src))
	copy(out, src)
	return out
}

// store inserts or replaces a cache entry and evicts LRU entries to stay within
// the byte budget. The stored entries slice is owned by the cache; callers must
// receive a clone (see cloneEntries) so eviction cannot corrupt a held slice.
// partial is the unterminated trailing line, kept so a later skip can reproduce
// the full cc.ReadFile result; it is excluded from offset and entries.
func (c *ParseCache) store(key string, size int64, mtime time.Time, offset int64, anchor []byte, entries []cc.Entry, partial *cc.Entry) {
	owned := cloneEntries(entries)
	b := estimateBytes(owned)
	var ownedPartial *cc.Entry
	if partial != nil {
		p := *partial
		ownedPartial = &p
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.totalSize -= old.bytes
	}
	c.clock++
	c.entries[key] = &cacheEntry{
		size:     size,
		mtime:    mtime,
		offset:   offset,
		anchor:   anchor,
		entries:  owned,
		partial:  ownedPartial,
		bytes:    b,
		lastUsed: c.clock,
	}
	c.totalSize += b
	c.evictToBudgetLocked()
}

func (c *ParseCache) touch(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.clock++
		e.lastUsed = c.clock
	}
	c.mu.Unlock()
}

func (c *ParseCache) evict(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.totalSize -= e.bytes
		delete(c.entries, key)
	}
	c.mu.Unlock()
}

// evictToBudgetLocked drops least-recently-used entries until totalSize is
// within maxBytes. Evicting an entry just makes its next ParseFile a full read.
// Caller must hold c.mu.
func (c *ParseCache) evictToBudgetLocked() {
	for c.totalSize > c.maxBytes && len(c.entries) > 0 {
		var oldestKey string
		var oldest int64 = 1<<63 - 1
		for k, e := range c.entries {
			if e.lastUsed < oldest {
				oldest = e.lastUsed
				oldestKey = k
			}
		}
		if e, ok := c.entries[oldestKey]; ok {
			c.totalSize -= e.bytes
			delete(c.entries, oldestKey)
		}
	}
}
