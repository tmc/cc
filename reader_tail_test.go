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

	entries, off, err := ReadFileWithOffset(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadFileWithOffset: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
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

	_, off, err := ReadFileWithOffset(ctx, path)
	if err != nil {
		t.Fatalf("ReadFileWithOffset: %v", err)
	}

	appendJSONL(t, path, tailRow("c"), tailRow("d"))

	tail, newOff, err := ReadFileFrom(ctx, path, off)
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

	tail, newOff, err := ReadFileFrom(ctx, path, fi.Size())
	if err != nil {
		t.Fatalf("ReadFileFrom: %v", err)
	}
	if len(tail) != 0 || newOff != fi.Size() {
		t.Fatalf("noop expected: entries=%d newOff=%d size=%d", len(tail), newOff, fi.Size())
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
	_, _, err := ReadFileFrom(ctx, path, bigOffset)
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
	_, _, err := ReadFileFrom(ctx, path, 5)
	if !errors.Is(err, ErrTailInvalid) {
		t.Fatalf("err = %v, want ErrTailInvalid", err)
	}
}

func TestReadFileFrom_UnterminatedLastLineExcluded(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "s.jsonl")
	writeJSONL(t, path, tailRow("a")) // complete line + newline
	_, off, _ := ReadFileWithOffset(ctx, path)

	// Append the bytes of entry b WITHOUT a trailing newline (writer mid-append).
	bLine, _ := json.Marshal(tailRow("b"))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write(bLine) // no newline yet
	f.Close()

	tail, newOff, err := ReadFileFrom(ctx, path, off)
	if err != nil {
		t.Fatalf("ReadFileFrom: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("partial line should not decode; got %d entries", len(tail))
	}
	if newOff != off {
		t.Fatalf("newOffset advanced past partial line: %d != %d", newOff, off)
	}

	// Complete entry b's line with its terminating newline; the next tail from
	// the same offset must now pick up exactly entry b.
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("\n"))
	f.Close()

	tail2, newOff2, err := ReadFileFrom(ctx, path, off)
	if err != nil {
		t.Fatalf("ReadFileFrom 2: %v", err)
	}
	full, _ := ReadFile(ctx, path)
	if len(full) != 2 {
		t.Fatalf("full = %d, want 2 (a, b)", len(full))
	}
	if !reflect.DeepEqual(tail2, full[1:]) {
		t.Fatalf("tail2 != full suffix after completion:\n tail2=%+v\n want=%+v", tail2, full[1:])
	}
	fi, _ := os.Stat(path)
	if newOff2 != fi.Size() {
		t.Fatalf("newOffset2 = %d, want %d", newOff2, fi.Size())
	}
}
