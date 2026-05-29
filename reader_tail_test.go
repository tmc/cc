package cc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// row builds a minimal valid Claude session entry line.
func tailRow(uuid string) map[string]any {
	return map[string]any{
		"type":      "user",
		"timestamp": "2026-04-20T10:00:00Z",
		"uuid":      uuid,
		"message":   map[string]any{"role": "user", "content": "hello " + uuid},
	}
}

func appendJSONL(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer f.Close()
	for _, r := range rows {
		b, _ := json.Marshal(r)
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func TestReadFileWithOffset_OffsetAtLastNewline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a"), tailRow("b"))

	entries, off, partial, err := ReadFileWithOffset(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFileWithOffset: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if partial != nil {
		t.Fatalf("partial = %+v, want nil (file ends in newline)", partial)
	}
	fi, _ := os.Stat(path)
	if off != fi.Size() {
		t.Fatalf("offset = %d, want file size %d (file ends in newline)", off, fi.Size())
	}
}

func TestReadFileFrom_AppendedLinesMatchFullSuffix(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a"), tailRow("b"))

	_, off, _, err := ReadFileWithOffset(ctx, path)
	if err != nil {
		t.Fatalf("ReadFileWithOffset: %v", err)
	}

	appendJSONL(t, path, tailRow("c"), tailRow("d"))

	tail, newOff, _, err := ReadFileFrom(ctx, path, off)
	if err != nil {
		t.Fatalf("ReadFileFrom: %v", err)
	}

	// Equivalence: tail entries must equal the suffix of a full re-read.
	full, _ := ReadFile(ctx, path)
	if len(full) != 4 {
		t.Fatalf("full = %d, want 4", len(full))
	}
	if !reflect.DeepEqual(tail, full[2:]) {
		t.Fatalf("tail entries != full suffix:\n tail=%+v\n want=%+v", tail, full[2:])
	}
	fi, _ := os.Stat(path)
	if newOff != fi.Size() {
		t.Fatalf("newOffset = %d, want %d", newOff, fi.Size())
	}
}

func TestReadFileFrom_OffsetEqualsSizeIsNoop(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a"))
	fi, _ := os.Stat(path)

	tail, newOff, partial, err := ReadFileFrom(ctx, path, fi.Size())
	if err != nil {
		t.Fatalf("ReadFileFrom: %v", err)
	}
	if len(tail) != 0 || newOff != fi.Size() || partial != nil {
		t.Fatalf("noop expected: entries=%d newOff=%d size=%d partial=%v", len(tail), newOff, fi.Size(), partial)
	}
}

func TestReadFileFrom_TruncationReturnsErrTailInvalid(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a"), tailRow("b"))
	fi, _ := os.Stat(path)
	bigOffset := fi.Size()

	// Truncate the file to fewer bytes than the recorded offset.
	if err := os.Truncate(path, fi.Size()/2); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, _, _, err := ReadFileFrom(ctx, path, bigOffset)
	if !errors.Is(err, ErrTailInvalid) {
		t.Fatalf("err = %v, want ErrTailInvalid", err)
	}
}

func TestReadFileFrom_NonBoundaryOffsetReturnsErrTailInvalid(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a"), tailRow("b"))

	// Offset 5 lands mid first line, not just past a newline.
	_, _, _, err := ReadFileFrom(ctx, path, 5)
	if !errors.Is(err, ErrTailInvalid) {
		t.Fatalf("err = %v, want ErrTailInvalid", err)
	}
}

// TestReadFileWithOffset_OversizeLineMatchesReadFile guards the incremental
// readers against indexing a session a plain ReadFile would skip: a single line
// at or above MaxLineSize must error in both, so the cached path and a cold scan
// agree on which files are unparseable.
func TestReadFileWithOffset_OversizeLineMatchesReadFile(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		contentN  int
		wantError bool
	}{
		{"just under max", MaxLineSize - 1, false},
		{"at max", MaxLineSize, true},
		{"over max", MaxLineSize + 100, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "s.jsonl")
			// A valid JSON line whose total length is contentN: pad the content
			// field so the whole object reaches the target size, then add '\n'.
			pad := tc.contentN - len(`{"type":"user","x":""}`)
			if pad < 0 {
				t.Fatalf("contentN %d too small", tc.contentN)
			}
			big := make([]byte, 0, tc.contentN+1)
			big = append(big, []byte(`{"type":"user","x":"`)...)
			for range pad {
				big = append(big, 'a')
			}
			big = append(big, []byte(`"}`)...)
			big = append(big, '\n')
			if err := os.WriteFile(path, big, 0o644); err != nil {
				t.Fatal(err)
			}

			_, _, _, offErr := ReadFileWithOffset(ctx, path)
			_, fileErr := ReadFile(ctx, path)
			// The two readers must agree on whether the file is parseable.
			if (offErr != nil) != (fileErr != nil) {
				t.Fatalf("disagreement: ReadFileWithOffset err=%v, ReadFile err=%v", offErr, fileErr)
			}
			if (offErr != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError = %v", offErr, tc.wantError)
			}
		})
	}
}

func TestReadFileFrom_UnterminatedLastLineReturnedNotCounted(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a")) // complete line + newline
	_, off, _, _ := ReadFileWithOffset(ctx, path)

	// Append the bytes of entry b WITHOUT a trailing newline (writer mid-append).
	bLine, _ := json.Marshal(tailRow("b"))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write(bLine) // no newline yet
	f.Close()

	tail, newOff, partial, err := ReadFileFrom(ctx, path, off)
	if err != nil {
		t.Fatalf("ReadFileFrom: %v", err)
	}
	// The complete-line set is empty and the offset does not advance past the
	// partial, so the line is re-read once it gains a newline.
	if len(tail) != 0 {
		t.Fatalf("complete tail should be empty; got %d entries", len(tail))
	}
	if newOff != off {
		t.Fatalf("newOffset advanced past partial line: %d != %d", newOff, off)
	}
	// The partial is decoded and returned so the result matches a full ReadFile.
	if partial == nil {
		t.Fatalf("partial = nil, want decoded entry b")
	}
	full, _ := ReadFile(ctx, path)
	if len(full) != 2 {
		t.Fatalf("full = %d, want 2 (a, b)", len(full))
	}
	if !reflect.DeepEqual(*partial, full[1]) {
		t.Fatalf("partial != full[1]:\n partial=%+v\n want=%+v", *partial, full[1])
	}

	// Complete entry b's line with its terminating newline; the next tail from
	// the same offset must now pick up exactly entry b once, with no partial.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("\n"))
	f.Close()

	tail2, newOff2, partial2, err := ReadFileFrom(ctx, path, off)
	if err != nil {
		t.Fatalf("ReadFileFrom 2: %v", err)
	}
	full2, _ := ReadFile(ctx, path)
	if len(full2) != 2 {
		t.Fatalf("full2 = %d, want 2 (a, b)", len(full2))
	}
	if !reflect.DeepEqual(tail2, full2[1:]) {
		t.Fatalf("tail2 != full suffix after completion:\n tail2=%+v\n want=%+v", tail2, full2[1:])
	}
	if partial2 != nil {
		t.Fatalf("partial2 = %+v, want nil after completion", partial2)
	}
	fi, _ := os.Stat(path)
	if newOff2 != fi.Size() {
		t.Fatalf("newOffset2 = %d, want %d", newOff2, fi.Size())
	}
}
