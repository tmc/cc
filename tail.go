package cc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/cc/internal/antigravityread"
	"github.com/tmc/cc/internal/opencoderead"
	"github.com/tmc/cc/internal/piread"
)

// Tail reads a growing session without losing the session ID, turn model or
// usage fold between polls. Use NewTail; the zero value is not usable. A Tail
// is not safe for concurrent use. It holds fold and summary state, not the
// complete transcript or an on-disk index.
type Tail struct {
	path           string
	info           os.FileInfo
	invalid        bool
	offset         int64
	anchor         []byte
	sessionID      string
	fold           codexFold
	assistant      Entry
	assistantIndex int
	count          int
	summary        *summarizer
}

// TailRevision replaces the entry at Index in the caller's accumulated
// transcript. Indices start at zero and count normalized entries, not lines.
type TailRevision struct {
	Index int
	Entry Entry
}

// TailUpdate is one successful poll. Apply Revisions to the accumulated
// transcript, then append Entries. If Replace is true, discard the previous
// transcript first. Summary covers committed entries (the entire snapshot for
// native readers). Partial is a decoded
// unterminated final line; it is not committed and must not be accumulated.
// Returned data is independent of the Tail and of earlier poll results.
type TailUpdate struct {
	Replace   bool
	Entries   []Entry
	Revisions []TailRevision
	Partial   *Entry
	Offset    int64
	Summary   SessionSummary
}

// NewTail prepares a tail for path. The file is opened by Poll. Paths are made
// absolute without resolving symlinks, so replacing the path is detectable.
func NewTail(path string) (*Tail, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("tail path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("tail path: %w", err)
	}
	t := &Tail{path: abs}
	t.Reset()
	return t, nil
}

// Reset discards offsets, fold state and summary. The next successful poll
// returns a replacement transcript. It does not read or modify the file.
func (t *Tail) Reset() {
	*t = Tail{path: t.path, assistantIndex: -1, summary: newSummarizer(t.path)}
}

// Poll reads complete appended lines and returns explicit revisions for usage
// arriving after an assistant message. On error, offsets, fold state and
// summary do not advance. Invalidation is latched until Reset. Rotation,
// truncation, a backward modification time, or a changed 256-byte anchor at the
// committed offset returns ErrTailInvalid; call Reset to read a replacement.
// The anchor does not detect rewrites elsewhere in a growing prefix. Writers
// must append; this is not an integrity check over the complete prefix.
//
// Pi, opencode and Antigravity use their native readers and return replacement
// snapshots on every poll because their storage is not this JSONL fold format.
func (t *Tail) Poll(ctx context.Context) (TailUpdate, error) {
	if t == nil || t.path == "" {
		return TailUpdate{}, fmt.Errorf("tail is not initialized")
	}
	if t.invalid {
		return TailUpdate{}, ErrTailInvalid
	}
	invalidate := func() (TailUpdate, error) { t.invalid = true; return TailUpdate{}, ErrTailInvalid }
	if err := ctx.Err(); err != nil {
		return TailUpdate{}, err
	}
	if piread.IsSessionPath(t.path) || opencoderead.IsSessionPath(t.path) || antigravityread.IsSessionPath(t.path) {
		entries, err := ReadFile(ctx, t.path)
		if err != nil {
			return TailUpdate{}, err
		}
		if err := ctx.Err(); err != nil {
			return TailUpdate{}, err
		}
		return TailUpdate{Replace: true, Entries: entries, Summary: Summarize(t.path, entries)}, nil
	}
	f, err := os.Open(t.path)
	if err != nil {
		return TailUpdate{}, fmt.Errorf("open tail: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return TailUpdate{}, err
	}
	if t.info != nil {
		if !os.SameFile(t.info, info) || info.Size() < t.offset || info.ModTime().Before(t.info.ModTime()) || (info.Size() == t.info.Size() && !info.ModTime().Equal(t.info.ModTime())) {
			return invalidate()
		}
		anchor, err := tailAnchor(f, t.offset)
		if err != nil {
			return TailUpdate{}, err
		}
		if !bytes.Equal(anchor, t.anchor) {
			return invalidate()
		}
	}
	// Work on a private copy so cancellation and read errors cannot commit
	// usage revisions, counters or summary changes.
	next := *t
	next.summary = newSummarizer(t.path)
	next.summary.s = t.summary.s
	next.summary.s.DistinctCWDs = append([]string(nil), t.summary.s.DistinctCWDs...)
	for cwd := range t.summary.seenCWDs {
		next.summary.seenCWDs[cwd] = true
	}
	if t.fold.total != nil {
		total := *t.fold.total
		next.fold.total = &total
	}
	if t.assistantIndex >= 0 {
		next.assistant, err = copyTailEntry(t.assistant)
		if err != nil {
			return TailUpdate{}, err
		}
		next.fold.assistant = next.assistant.Message
	}
	if _, err = f.Seek(t.offset, io.SeekStart); err != nil {
		return TailUpdate{}, err
	}
	update := TailUpdate{Replace: t.info == nil}
	// Read through the statted extent; concurrent appends are picked up next
	// poll. Complete lines only advance the offset.
	br := bufio.NewReaderSize(io.LimitReader(f, info.Size()-t.offset), 4096)
	var revision *TailRevision
	for {
		if err := ctx.Err(); err != nil {
			return TailUpdate{}, err
		}
		line, complete, readErr := readLine(br)
		if readErr != nil && readErr != io.EOF {
			return TailUpdate{}, readErr
		}
		e, ok := decodeLine(line)
		if !complete {
			if ok {
				if e.SessionID == "" {
					e.SessionID = next.sessionID
				}
				fold := next.fold
				fold.see(e)
				update.Partial = &e
			}
			break
		}
		next.offset += int64(len(line) + 1)
		if ok {
			if e.SessionID != "" {
				next.sessionID = e.SessionID
			} else {
				e.SessionID = next.sessionID
			}
			consumed := false
			if e.Type == "system" && e.Subtype == "token_count" && e.Usage != nil && next.fold.add(e) {
				if next.assistantIndex >= 0 && next.assistantIndex < t.count {
					revision = &TailRevision{Index: next.assistantIndex, Entry: next.assistant}
				}
				if e.RateLimits == nil {
					consumed = true
				} else {
					e.Usage, e.UsageTotal = nil, nil
				}
			}
			if !consumed {
				next.fold.see(e)
				if e.Message != nil && e.Message.Role == "assistant" {
					next.assistant = e
					next.assistantIndex = next.count
				}
				next.summary.add(e)
				update.Entries = append(update.Entries, e)
				next.count++
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if revision != nil {
		update.Revisions = append(update.Revisions, *revision)
	}
	// Detach outputs from mutable fold state, including RawMessage slices.
	for i := range update.Entries {
		update.Entries[i], err = copyTailEntry(update.Entries[i])
		if err != nil {
			return TailUpdate{}, err
		}
	}
	for i := range update.Revisions {
		update.Revisions[i].Entry, err = copyTailEntry(update.Revisions[i].Entry)
		if err != nil {
			return TailUpdate{}, err
		}
	}
	after, err := f.Stat()
	if err != nil {
		return TailUpdate{}, err
	}
	if after.Size() < info.Size() || after.ModTime().Before(info.ModTime()) || (after.Size() == info.Size() && !after.ModTime().Equal(info.ModTime())) {
		return invalidate()
	}
	current, err := os.Stat(t.path)
	if err != nil {
		return TailUpdate{}, err
	}
	if !os.SameFile(info, current) {
		return invalidate()
	}
	next.anchor, err = tailAnchor(f, next.offset)
	if err != nil {
		return TailUpdate{}, err
	}
	if err := ctx.Err(); err != nil {
		return TailUpdate{}, err
	}
	next.info = info
	update.Offset = next.offset
	update.Summary = next.summary.s
	update.Summary.DistinctCWDs = append([]string(nil), next.summary.s.DistinctCWDs...)
	*t = next
	return update, nil
}

func tailAnchor(f *os.File, offset int64) ([]byte, error) {
	n := int64(256)
	if offset < n {
		n = offset
	}
	anchor := make([]byte, n)
	if n == 0 {
		return anchor, nil
	}
	_, err := f.ReadAt(anchor, offset-n)
	return anchor, err
}

func copyTailEntry(e Entry) (Entry, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return Entry{}, fmt.Errorf("copy tail entry: %w", err)
	}
	var copy Entry
	if err := json.Unmarshal(data, &copy); err != nil {
		return Entry{}, err
	}
	// JSON cloning detaches nested pointers, but marshaling compacts raw JSON
	// and omits internal fields. Preserve those verbatim as well.
	copy.ThinkingMetadata = bytes.Clone(e.ThinkingMetadata)
	copy.Todos = bytes.Clone(e.Todos)
	copy.Data = bytes.Clone(e.Data)
	copy.Snapshot = bytes.Clone(e.Snapshot)
	if e.Message != nil {
		copy.Message.Content = bytes.Clone(e.Message.Content)
	}
	if e.Attachment != nil {
		copy.Attachment.Raw = bytes.Clone(e.Attachment.Raw)
	}
	if e.ToolUseResult != nil {
		copy.ToolUseResult.Text = e.ToolUseResult.Text
		copy.ToolUseResult.Content = bytes.Clone(e.ToolUseResult.Content)
	}
	copy.Timestamp = e.Timestamp
	copy.TurnModel = e.TurnModel
	if e.UsageTotal != nil {
		total := *e.UsageTotal
		if total.CacheCreation != nil {
			detail := *total.CacheCreation
			total.CacheCreation = &detail
		}
		copy.UsageTotal = &total
	}
	return copy, nil
}
