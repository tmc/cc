package cc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/codexread"
	"github.com/tmc/cc/internal/opencoderead"
	"github.com/tmc/cc/internal/piread"
)

// Reader reads entries from a JSONL session file.
// The zero value is not usable; use NewReader.
type Reader struct {
	ctx     context.Context
	scanner *bufio.Scanner
	err     error
	entry   Entry
	n       int

	currentSessionID string
	pending          Entry
	hasPending       bool
	buffered         Entry
	hasBuffered      bool
}

// NewReader creates a Reader from an io.Reader. The context is checked
// cooperatively during Next so callers can cancel long reads.
func NewReader(ctx context.Context, r io.Reader) *Reader {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, initialBufferSize), MaxLineSize)
	return &Reader{ctx: ctx, scanner: s}
}

// Next advances to the next entry. Returns false at EOF, on error, or when
// the reader's context is canceled.
func (r *Reader) Next() bool {
	if r.hasBuffered {
		r.entry = r.buffered
		r.hasBuffered = false
		return true
	}
	if r.hasPending {
		r.entry = r.pending
		r.hasPending = false
		return true
	}
	for r.scanner.Scan() {
		r.n++
		if r.n%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				r.err = err
				return false
			}
		}
		line := r.scanner.Text()
		if line == "" {
			continue
		}
		entry, ok := decodeEntryLine([]byte(line))
		if !ok {
			continue
		}
		if entry.SessionID != "" {
			r.currentSessionID = entry.SessionID
		} else if r.currentSessionID != "" {
			entry.SessionID = r.currentSessionID
		}
		if entry.Type == "system" && entry.Subtype == "token_count" && entry.Usage != nil {
			if r.hasPending && r.pending.Message != nil && r.pending.Message.Role == "assistant" && r.pending.Message.Usage == nil {
				r.pending.Message.Usage = entry.Usage
			}
			continue
		}
		if r.hasPending {
			r.buffered = entry
			r.hasBuffered = true
			r.entry = r.pending
			r.pending = Entry{}
			r.hasPending = false
			return true
		}
		if entry.Message != nil && entry.Message.Role == "assistant" {
			r.pending = entry
			r.hasPending = true
			continue
		}
		r.entry = entry
		return true
	}
	if r.hasPending {
		r.entry = r.pending
		r.pending = Entry{}
		r.hasPending = false
		return true
	}
	r.err = r.scanner.Err()
	return false
}

func decodeEntryLine(line []byte) (Entry, bool) {
	if entry, isCodex, ok := codexread.Decode(line); isCodex {
		return entry, ok
	}
	var entry Entry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Entry{}, false
	}
	return entry, true
}

// Entry returns the current entry.
func (r *Reader) Entry() Entry { return r.entry }

// Err returns any error from scanning.
func (r *Reader) Err() error { return r.err }

// ReadAll reads all entries from the reader.
func ReadAll(ctx context.Context, r io.Reader) ([]Entry, error) {
	entries, _, partial, err := readComplete(ctx, r)
	if partial != nil {
		entries = append(entries, *partial)
	}
	return entries, err
}

// ReadFile reads all entries from a JSONL file.
func ReadFile(ctx context.Context, path string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opencoderead.IsSessionPath(path) {
		return opencoderead.ReadFile(ctx, path)
	}
	if piread.IsSessionPath(path) {
		return piread.ReadFile(ctx, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAll(ctx, f)
}

// ErrTailInvalid reports that an offset passed to [ReadFileFrom] cannot be
// safely tailed — the file is shorter than the offset (truncation or rewrite),
// or the offset does not sit just past a newline. Callers should fall back to a
// full [ReadFileWithOffset].
var ErrTailInvalid = errors.New("tail offset invalid; reparse from start")

// ReadFileFrom reads the entries appended to a JSONL file after the given byte
// offset, which must point just past a line terminator (or be 0). It returns
// the new complete entries and the byte offset just past the last complete line
// read. A trailing line without a final newline is treated as still being
// written: it is decoded and returned separately as partial (nil if absent or
// undecodable), but is not included in newOffset, so it is re-read once
// complete. Callers that cache by offset must store only entries and return
// entries+partial — folding partial into the cache would double-count it when
// the line later completes.
//
// Because each JSONL line decodes independently (see decodeEntryLine), the
// entries (plus partial) returned are exactly those a full [ReadFile] would
// yield for the same byte range. ReadFileFrom returns [ErrTailInvalid] when
// offset is past the file size or not on a line boundary; the caller should
// reparse the whole file in that case.
func ReadFileFrom(ctx context.Context, path string, offset int64) (entries []Entry, newOffset int64, partial *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, 0, nil, err
	}
	size := fi.Size()
	if offset > size {
		return nil, 0, nil, ErrTailInvalid // file shrank: truncation or rewrite.
	}
	if offset == size {
		return nil, offset, nil, nil // nothing appended.
	}
	if offset > 0 {
		// Confirm the offset sits just past a newline, so we start on a clean
		// line boundary rather than mid-line.
		var b [1]byte
		if _, err := f.ReadAt(b[:], offset-1); err != nil {
			return nil, 0, nil, err
		}
		if b[0] != '\n' {
			return nil, 0, nil, ErrTailInvalid
		}
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, nil, err
	}
	entries, consumed, partial, err := readComplete(ctx, f)
	if err != nil {
		return nil, 0, nil, err
	}
	return entries, offset + consumed, partial, nil
}

// ReadFileWithOffset reads all entries from a JSONL file and also returns the
// byte offset just past the last complete (newline-terminated) line, suitable
// for a later [ReadFileFrom]. A trailing line without a final newline is
// decoded and returned as partial (nil if absent or undecodable) but is not
// reflected in the offset, so it is re-read once completed. See [ReadFileFrom]
// for the caching contract on partial.
func ReadFileWithOffset(ctx context.Context, path string) (entries []Entry, offset int64, partial *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	defer f.Close()
	return readComplete(ctx, f)
}

// readComplete scans r, decoding each complete newline-terminated line, and
// returns the decoded entries plus the number of bytes consumed up to and
// including the last newline. Bytes after the final newline (an unterminated
// trailing line) are not counted in consumed — so the offset always lands on a
// line boundary — but are decoded and returned as partial (nil if absent or
// undecodable), matching what [ReadFile]'s scanner yields for the final token.
// A single line longer than [MaxLineSize] returns the entries decoded so far
// plus [bufio.ErrTooLong], exactly as [ReadFile] does, so all readers agree on
// which files are unparseable.
func readComplete(ctx context.Context, r io.Reader) (entries []Entry, consumed int64, partial *Entry, err error) {
	br := bufio.NewReaderSize(r, initialBufferSize)
	n := 0
	currentSessionID := ""
	lastAssistant := -1
	for {
		line, complete, readErr := readLine(br)
		if readErr != nil && readErr != io.EOF {
			// ErrTooLong (line content reached MaxLineSize) or an I/O error;
			// either way report it just as ReadFile's scanner would, with the
			// entries decoded so far.
			return entries, consumed, nil, readErr
		}
		if complete {
			consumed += int64(len(line)) + 1 // include the trailing newline.
			n++
			if n%256 == 0 {
				if cerr := ctx.Err(); cerr != nil {
					return entries, consumed, nil, cerr
				}
			}
			if entry, ok := decodeLine(line); ok {
				if entry.SessionID != "" {
					currentSessionID = entry.SessionID
				} else if currentSessionID != "" {
					entry.SessionID = currentSessionID
				}
				if entry.Type == "system" && entry.Subtype == "token_count" && entry.Usage != nil {
					if lastAssistant >= 0 && lastAssistant < len(entries) {
						if msg := entries[lastAssistant].Message; msg != nil && msg.Role == "assistant" {
							msg.Usage = entry.Usage
						}
					}
					continue
				}
				entries = append(entries, entry)
				if entry.Message != nil && entry.Message.Role == "assistant" {
					lastAssistant = len(entries) - 1
				}
			}
		} else if len(line) > 0 {
			// An unterminated final line: decode it for the returned set
			// (matching ReadFile) but leave it out of consumed so it is re-read
			// once a newline lands.
			if entry, ok := decodeLine(line); ok {
				partial = &entry
			}
		}
		if readErr == io.EOF {
			return entries, consumed, partial, nil
		}
	}
}

// readLine returns the next line from br without its trailing newline. complete
// reports whether the line was newline-terminated (false for an unterminated
// final line at EOF). It returns [bufio.ErrTooLong] when a single line's
// content reaches [MaxLineSize] — matching the bufio.Scanner cap [ReadFile]
// uses — so memory stays bounded even on a pathological multi-gigabyte line.
func readLine(br *bufio.Reader) (line []byte, complete bool, err error) {
	for {
		frag, e := br.ReadSlice('\n')
		if e == nil {
			if line == nil {
				return frag[:len(frag)-1], true, nil // common case: no fragment buffering.
			}
			line = append(line, frag[:len(frag)-1]...)
			if len(line) >= MaxLineSize {
				return nil, false, bufio.ErrTooLong
			}
			return line, true, nil
		}
		if e == bufio.ErrBufferFull {
			line = append(line, frag...)
			if len(line) >= MaxLineSize {
				return nil, false, bufio.ErrTooLong
			}
			continue
		}
		// e is io.EOF or an I/O error: frag holds the trailing unterminated bytes.
		line = append(line, frag...)
		if len(line) >= MaxLineSize {
			return nil, false, bufio.ErrTooLong
		}
		return line, false, e
	}
}

// decodeLine trims a trailing CR and skips an empty line, then decodes it the
// same way [Reader.Next] does, so terminated and unterminated lines decode
// identically.
func decodeLine(content []byte) (Entry, bool) {
	if len(content) > 0 && content[len(content)-1] == '\r' {
		content = content[:len(content)-1]
	}
	if len(content) == 0 {
		return Entry{}, false
	}
	return decodeEntryLine(content)
}

// ReadFileWithSubagents reads a session JSONL file and merges entries from any
// subagent files found at <path-without-.jsonl>/subagents/agent-*.jsonl.
// Subagent entries are tagged with AgentID (from the filename) and IsSidechain=true.
// The merged result is sorted by timestamp.
func ReadFileWithSubagents(ctx context.Context, path string) ([]Entry, error) {
	entries, err := ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	subs, err := ReadSubagents(ctx, path)
	if err != nil {
		return entries, nil
	}
	entries = append(entries, subs...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.Before(entries[j].Timestamp)
	})
	return entries, nil
}

// ReadSubagents reads entries from subagent files under
// <path-without-.jsonl>/subagents/agent-*.jsonl. Each entry is tagged with
// AgentID derived from the filename and IsSidechain=true. Returns a nil slice
// and nil error if the subagents directory does not exist.
func ReadSubagents(ctx context.Context, path string) ([]Entry, error) {
	subagentDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	infos, err := os.ReadDir(subagentDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []Entry
	for _, fi := range infos {
		name := fi.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-acompact") {
			continue
		}
		sub, err := ReadFile(ctx, filepath.Join(subagentDir, name))
		if err != nil {
			continue
		}
		agentID := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".jsonl")
		for i := range sub {
			if sub[i].AgentID == "" {
				sub[i].AgentID = agentID
			}
			sub[i].IsSidechain = true
		}
		entries = append(entries, sub...)
	}
	return entries, nil
}

// SessionSummary holds summarized metadata for a session file.
// GitBranch is the branch recorded in the session entries; the embedded
// GitContext fields are resolved from the latest CWD on the local
// filesystem and may differ if the worktree has since moved or changed.
type SessionSummary struct {
	SessionID    string   `json:"session_id"`
	File         string   `json:"file"`
	Project      string   `json:"project"`
	CWD          string   `json:"cwd,omitempty"`
	DistinctCWDs []string `json:"distinct_cwds,omitempty"`
	GitBranch    string   `json:"git_branch,omitempty"`
	ccgit.GitContext
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
	seenCWDs := map[string]bool{}
	for _, e := range entries {
		s.TotalLines++
		if e.SessionID != "" && s.SessionID == "" {
			s.SessionID = e.SessionID
		}
		if e.Version != "" && s.Version == "" {
			s.Version = e.Version
		}
		if e.CWD != "" {
			s.CWD = e.CWD
			if !seenCWDs[e.CWD] {
				seenCWDs[e.CWD] = true
				s.DistinctCWDs = append(s.DistinctCWDs, e.CWD)
			}
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
				if e.Message.IsToolResultOnly() {
					break
				}
				s.UserMessages++
				if s.FirstPrompt == "" && !e.IsMeta {
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
	if s.CWD != "" {
		if ctx, err := ccgit.ResolveGitContext(s.CWD); err == nil {
			s.GitContext = ctx
		}
	}
	return s
}

// ExtractText pulls the first text content from a message content field.
func ExtractText(raw json.RawMessage) string {
	return collapseWhitespace(ExtractAnyText(raw), 200)
}

func collapseWhitespace(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// FindSessionFiles finds session files under ~/.claude/projects/,
// ~/.gemini/projects/, ~/.codex/sessions/, and opencode storage. It excludes subagent files,
// filters by modification time, and stops early when ctx is canceled.
func FindSessionFiles(ctx context.Context, since time.Duration, project string) ([]string, error) {
	ch, err := ccpaths.ClaudeHome()
	if err != nil {
		return nil, err
	}
	gh, _ := ccpaths.GeminiHome()
	xh, _ := ccpaths.CodexHome()
	oh, _ := ccpaths.OpenCodeHome()
	ph, _ := ccpaths.PiHome()

	cutoff := time.Now().Add(-since)
	var files []string

	type rootDir struct {
		path string
		kind string
	}

	dirs := []rootDir{{path: filepath.Join(ch, "projects"), kind: "claude"}}
	if gh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(gh, "projects"), kind: "gemini"})
	}
	if xh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(xh, "sessions"), kind: "codex"})
	}
	if oh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(oh, "storage", "session"), kind: "opencode"})
	}
	if ph != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(ph, "sessions"), kind: "pi"})
	}

	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := filepath.Walk(dir.path, func(path string, info os.FileInfo, err error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err != nil {
				return nil
			}
			if info.IsDir() && info.Name() == "subagents" {
				return filepath.SkipDir
			}
			if dir.kind == "opencode" {
				if !opencoderead.IsSessionPath(path) {
					return nil
				}
			} else if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				return nil
			}
			if project != "" {
				q := strings.ToLower(project)
				switch dir.kind {
				case "codex":
					if !codexPathMatchesProject(ctx, path, q) {
						return nil
					}
				case "opencode":
					if !openCodePathMatchesProject(ctx, path, q) {
						return nil
					}
				case "pi":
					if !piPathMatchesProject(ctx, path, q) {
						return nil
					}
				default:
					rel, _ := filepath.Rel(dir.path, path)
					if !strings.Contains(strings.ToLower(rel), q) {
						return nil
					}
				}
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// cwdPathMatchesProject matches a session against a project query for formats
// (codex, opencode, pi) whose on-disk directory encodes the cwd lossily, so the
// session's own cwd is authoritative and the encoded path is only a fallback.
// extra, when non-nil, contributes additional per-entry strings to match (e.g.
// opencode's custom title).
func cwdPathMatchesProject(ctx context.Context, path, query string, extra func(Entry) string) bool {
	entries, err := ReadFile(ctx, path)
	if err != nil {
		return strings.Contains(strings.ToLower(path), query)
	}
	for _, e := range entries {
		if e.CWD != "" && strings.Contains(strings.ToLower(e.CWD), query) {
			return true
		}
		if extra != nil {
			if s := extra(e); s != "" && strings.Contains(strings.ToLower(s), query) {
				return true
			}
		}
	}
	return strings.Contains(strings.ToLower(path), query)
}

func openCodePathMatchesProject(ctx context.Context, path, query string) bool {
	return cwdPathMatchesProject(ctx, path, query, func(e Entry) string { return e.CustomTitle })
}

func codexPathMatchesProject(ctx context.Context, path, query string) bool {
	return cwdPathMatchesProject(ctx, path, query, nil)
}

func piPathMatchesProject(ctx context.Context, path, query string) bool {
	return cwdPathMatchesProject(ctx, path, query, nil)
}
