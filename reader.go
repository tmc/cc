package cc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc/ccgit"
	"github.com/tmc/cc/ccpaths"
	"github.com/tmc/cc/internal/antigravityread"
	"github.com/tmc/cc/internal/codexread"
	"github.com/tmc/cc/internal/geminiread"
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
	usage            codexFold
}

// codexFold attaches the facts Codex reports on their own lines — the token
// counts of a finished request and the model of the current turn — to the
// assistant messages they belong to.
//
// Codex reports usage as one token_count event per request rather than a field
// on the message, and the events do not line up one-to-one with assistant
// entries: several can arrive between two of them, tool calls and reasoning sit
// in between, and a rate-limit refresh re-sends the last counter unchanged. So
// the fold sums successive events into the most recent assistant message and
// skips a repeat whose running total has not moved, which would otherwise count
// one request's tokens twice.
//
// The model arrives the other way round, on a turn_context line before the
// turn's messages, so it is stamped onto each assistant message as it is seen.
type codexFold struct {
	assistant *Message
	total     *Usage
	model     string
}

// see records entry as the message later token_count entries belong to, and
// stamps it with the turn's model when the harness records one per turn.
func (f *codexFold) see(entry Entry) {
	if entry.TurnModel != "" {
		f.model = entry.TurnModel
	}
	if entry.Message == nil || entry.Message.Role != "assistant" {
		return
	}
	f.assistant = entry.Message
	if f.assistant.Model == "" {
		f.assistant.Model = f.model
	}
}

// add folds one token_count entry into the preceding assistant message and
// reports whether it was consumed. Because Message is a pointer, an entry
// already returned to the caller gains the usage too — the alternative is
// dropping it, since the counter always arrives after the message it describes.
//
// A repeated counter is consumed and discarded. An event with no assistant
// message before it is not consumed: it describes a request whose message is
// not in this file — Codex emits them for a sub-agent turn, and a tail read
// starts mid-session — and attributing it to a later message would bill the
// wrong request, so the caller emits it as its own entry instead and
// [github.com/tmc/cc/ccusage.FromEntries] counts it unattributed.
func (f *codexFold) add(entry Entry) bool {
	if sameUsage(f.total, entry.UsageTotal) {
		return true // running total unmoved: the same request, reported twice.
	}
	if entry.UsageTotal != nil {
		f.total = entry.UsageTotal
	}
	if f.assistant == nil {
		return false
	}
	f.assistant.Usage = addUsage(f.assistant.Usage, entry.Usage)
	return true
}

// sameUsage reports whether a and b are both present and carry the same four
// token counts.
func sameUsage(a, b *Usage) bool {
	if a == nil || b == nil {
		return false
	}
	return a.InputTokens == b.InputTokens && a.OutputTokens == b.OutputTokens &&
		a.CacheReadInputTokens == b.CacheReadInputTokens &&
		a.CacheCreationInputTokens == b.CacheCreationInputTokens
}

// addUsage returns the sum of dst and src without mutating either: the usage a
// token_count entry carries is handed to callers as part of that entry, so it
// must not become an accumulator.
func addUsage(dst, src *Usage) *Usage {
	if src == nil {
		return dst
	}
	sum := *src
	if dst != nil {
		sum = *dst
		sum.InputTokens += src.InputTokens
		sum.OutputTokens += src.OutputTokens
		sum.CacheReadInputTokens += src.CacheReadInputTokens
		sum.CacheCreationInputTokens += src.CacheCreationInputTokens
	}
	return &sum
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
		line := r.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		entry, ok := decodeEntryLine(line)
		if !ok {
			continue
		}
		if entry.SessionID != "" {
			r.currentSessionID = entry.SessionID
		} else if r.currentSessionID != "" {
			entry.SessionID = r.currentSessionID
		}
		if entry.Type == "system" && entry.Subtype == "token_count" && entry.Usage != nil {
			if r.usage.add(entry) {
				if entry.RateLimits == nil {
					continue
				}
				entry.Usage, entry.UsageTotal = nil, nil
			}
			// Unattributed: fall through so the tokens stay in the stream.
		}
		r.usage.see(entry)
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

// decodeEntryLine decodes one JSONL line. A decoded entry has its spilled tool
// output resolved (see resolvePersistedOutput), which reads the sidecar file
// named by the line itself; making that an invariant of decoding is what keeps
// every reader — streaming, whole-file, and tailing — from silently rendering a
// two-kilobyte preview as if it were the whole result.
func decodeEntryLine(line []byte) (Entry, bool) {
	// Decode the outer object once, preserving native fields and the Codex
	// payload. Rollouts may interleave both formats, so dispatch per line.
	var decoded struct {
		Entry
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(line, &decoded); err != nil {
		return Entry{}, false
	}
	if entry, isCodex, ok := codexread.DecodePayload(decoded.Type, decoded.Timestamp, decoded.Payload); isCodex {
		return entry, ok
	}
	resolvePersistedOutput(&decoded.Entry)
	return decoded.Entry, true
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
	if antigravityread.IsSessionPath(path) {
		return antigravityread.ReadFile(ctx, path)
	}
	if geminiread.IsSessionPath(path) {
		return geminiread.ReadFile(ctx, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadAll(ctx, f)
}

// ErrTailInvalid also reports file invalidation from [Tail.Poll]. Reset that
// Tail before polling again. For [ReadFileFrom], it reports an offset that cannot be
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
// Each JSONL line decodes independently (see decodeEntryLine), so the entries
// (plus partial) returned are those a full [ReadFile] would yield for the same
// byte range, with one exception: a Codex token_count line describes the
// request before it, so one that starts a tail read, or that is still being
// written, has no assistant entry to attach to and is returned as its own
// unattributed entry. Its tokens are counted either way; only their attribution
// waits for a read that sees the whole session. ReadFileFrom returns
// [ErrTailInvalid] when offset is past the file size or not on a line boundary;
// the caller should reparse the whole file in that case. Opencode sessions
// store messages in separate files: offset zero reads a full snapshot, and
// every nonzero offset returns ErrTailInvalid because byte offsets cannot
// track changes to those files.
func ReadFileFrom(ctx context.Context, path string, offset int64) (entries []Entry, newOffset int64, partial *Entry, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, err
	}
	if opencoderead.IsSessionPath(path) {
		// Messages and parts live in separate files; the metadata file's
		// byte offset cannot identify changes to the session.
		if offset != 0 {
			return nil, 0, nil, ErrTailInvalid
		}
		entries, err := opencoderead.ReadFile(ctx, path)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, 0, nil, err
		}
		return entries, info.Size(), nil, nil
	}
	if piread.IsSessionPath(path) {
		entries, newOffset, err := piread.ReadFileFrom(ctx, path, offset)
		return entries, newOffset, nil, err
	}
	if antigravityread.IsSessionPath(path) || geminiread.IsSessionPath(path) {
		entries, err := ReadFile(ctx, path)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, 0, nil, err
		}
		return entries, info.Size(), nil, nil
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
	if opencoderead.IsSessionPath(path) {
		return ReadFileFrom(ctx, path, 0)
	}
	if piread.IsSessionPath(path) {
		entries, err := piread.ReadFile(ctx, path)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, 0, nil, err
		}
		return entries, info.Size(), nil, nil
	}
	if antigravityread.IsSessionPath(path) || geminiread.IsSessionPath(path) {
		entries, err := ReadFile(ctx, path)
		if err != nil {
			return nil, 0, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, 0, nil, err
		}
		return entries, info.Size(), nil, nil
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
// A partial is never folded into an earlier entry, even when it is a Codex
// token_count: the entries it would mutate are cached by callers that will
// re-read the line once it completes.
// A single line longer than [MaxLineSize] returns the entries decoded so far
// plus [bufio.ErrTooLong], exactly as [ReadFile] does, so all readers agree on
// which files are unparseable.
func readComplete(ctx context.Context, r io.Reader) (entries []Entry, consumed int64, partial *Entry, err error) {
	br := bufio.NewReaderSize(r, initialBufferSize)
	n := 0
	currentSessionID := ""
	var usage codexFold
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
					if usage.add(entry) {
						if entry.RateLimits == nil {
							continue
						}
						entry.Usage, entry.UsageTotal = nil, nil
					}
					// Unattributed: fall through so the tokens stay in the stream.
				}
				entries = append(entries, entry)
				usage.see(entry)
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
		return entries, err
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".jsonl") {
		return nil, nil
	}
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
		if fi.IsDir() || !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if strings.HasPrefix(name, "agent-acompact") {
			continue
		}
		sub, err := ReadFile(ctx, filepath.Join(subagentDir, name))
		if err != nil {
			return entries, fmt.Errorf("read subagent %q: %w", name, err)
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
// GitContext fields are left empty by summarization. Callers can populate them
// with ccgit.ResolveGitContext using the latest CWD.
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

// Summarize builds a SessionSummary from entries without running git.
// To include local git metadata, assign ccgit.ResolveGitContext(s.CWD)
// to the returned summary's GitContext, handling the error as appropriate.
func Summarize(file string, entries []Entry) SessionSummary {
	sum := newSummarizer(file)
	for _, e := range entries {
		sum.add(e)
	}
	return sum.s
}

// SummarizeFile summarizes the session at path without holding the decoded
// transcript in memory: entries stream through the summary one line at a time.
// It yields the same result as [ReadFile] followed by [Summarize].
func SummarizeFile(ctx context.Context, path string) (SessionSummary, error) {
	if opencoderead.IsSessionPath(path) || piread.IsSessionPath(path) || antigravityread.IsSessionPath(path) || geminiread.IsSessionPath(path) {
		entries, err := ReadFile(ctx, path)
		if err != nil {
			return SessionSummary{}, err
		}
		return Summarize(path, entries), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()
	sum := newSummarizer(path)
	r := NewReader(ctx, f)
	for r.Next() {
		sum.add(r.Entry())
	}
	if err := r.Err(); err != nil {
		return SessionSummary{}, err
	}
	return sum.s, nil
}

// summarizer folds entries into a SessionSummary incrementally.
type summarizer struct {
	s        SessionSummary
	seenCWDs map[string]bool
}

func newSummarizer(file string) *summarizer {
	return &summarizer{s: SessionSummary{File: file}, seenCWDs: map[string]bool{}}
}

func (sum *summarizer) add(e Entry) {
	s, seenCWDs := &sum.s, sum.seenCWDs
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
			if s.FirstPrompt == "" && !e.IsMeta && !IsHarnessPreamble(e.Message.TextContent()) {
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
	ah, _ := ccpaths.AntigravityHome()

	cutoff := time.Now().Add(-since)
	var files []string

	type rootDir struct {
		path string
		kind string
	}

	dirs := []rootDir{{path: filepath.Join(ch, "projects"), kind: "claude"}}
	if gh != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(gh, "projects"), kind: "gemini"})
		dirs = append(dirs, rootDir{path: filepath.Join(gh, "tmp"), kind: "gemini-cli"})
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
	if ah != "" {
		dirs = append(dirs, rootDir{path: filepath.Join(ah, "brain"), kind: "antigravity"})
	}
	if gh != "" {
		cliPath := filepath.Join(gh, "antigravity-cli", "brain")
		if info, err := os.Stat(cliPath); err == nil && info.IsDir() {
			dirs = append(dirs, rootDir{path: cliPath, kind: "antigravity"})
		}
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
			if dir.kind == "antigravity" {
				if antigravityread.IsSessionPath(path) {
					mod := info.ModTime()
					if info.IsDir() {
						// The directory's mtime goes stale: appending to the
						// nested transcript only touches its own parent.
						if _, st := antigravityread.TranscriptFile(path); st != nil {
							mod = st.ModTime()
						}
					}
					if mod.Before(cutoff) {
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
					if project != "" {
						q := strings.ToLower(project)
						targetDir := path
						if !info.IsDir() {
							targetDir = filepath.Dir(filepath.Dir(filepath.Dir(path)))
						}
						if !antigravityPathMatchesProject(ctx, targetDir, q) {
							if info.IsDir() {
								return filepath.SkipDir
							}
							return nil
						}
					}
					files = append(files, path)
					if info.IsDir() {
						return filepath.SkipDir
					}
				}
				return nil
			}
			if dir.kind == "opencode" {
				if !opencoderead.IsSessionPath(path) {
					return nil
				}
			} else if dir.kind == "gemini-cli" {
				if !geminiread.IsSessionPath(path) {
					return nil
				}
			} else if !strings.HasSuffix(path, ".jsonl") {
				return nil
			}
			mod := info.ModTime()
			if dir.kind == "opencode" {
				mod = openCodeModTime(path, mod)
			}
			if mod.Before(cutoff) {
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

// openCodeModTime reports the newest file modification belonging to an
// opencode session. Its session record is created once, while new messages
// and parts are written in sibling directories, so the record's mtime alone
// would hide an active session from a -since query.
func openCodeModTime(path string, fallback time.Time) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return fallback
	}
	var session struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &session) != nil || session.ID == "" {
		return fallback
	}
	storage := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	messageDir := filepath.Join(storage, "message", session.ID)
	infos, err := os.ReadDir(messageDir)
	if err != nil {
		return fallback
	}
	mod := fallback
	for _, info := range infos {
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".json") {
			continue
		}
		messagePath := filepath.Join(messageDir, info.Name())
		if fi, err := os.Stat(messagePath); err == nil && fi.ModTime().After(mod) {
			mod = fi.ModTime()
		}
		data, err := os.ReadFile(messagePath)
		if err != nil {
			continue
		}
		var message struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &message) != nil || message.ID == "" {
			continue
		}
		parts, err := os.ReadDir(filepath.Join(storage, "part", message.ID))
		if err != nil {
			continue
		}
		for _, part := range parts {
			if part.IsDir() || !strings.HasSuffix(part.Name(), ".json") {
				continue
			}
			if fi, err := os.Stat(filepath.Join(storage, "part", message.ID, part.Name())); err == nil && fi.ModTime().After(mod) {
				mod = fi.ModTime()
			}
		}
	}
	return mod
}

// cwdPathMatchesProject matches a session against a project query for formats
// (codex, opencode, pi) whose on-disk directory encodes the cwd lossily, so the
// session's own cwd is authoritative and the encoded path is only a fallback.
// extra, when non-nil, contributes additional per-entry strings to match (e.g.
// opencode's custom title).
func cwdPathMatchesProject(ctx context.Context, path, query string, extra func(Entry) string) bool {
	if query == "" {
		return true
	}
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

func antigravityPathMatchesProject(ctx context.Context, path, query string) bool {
	return cwdPathMatchesProject(ctx, path, query, nil)
}
