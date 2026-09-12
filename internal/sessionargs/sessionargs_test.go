package sessionargs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(a, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.jsonl")

	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "one file", args: []string{a}, want: "a\n"},
		{name: "two files", args: []string{a, b}, want: "a\nb\n"},
		{name: "unreadable file is skipped", args: []string{missing, b}, want: "b\n"},
		{name: "all unreadable", args: []string{missing}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readers, closers, err := Open(context.Background(), tt.args, "")
			if tt.wantErr {
				if err == nil {
					t.Fatal("Open succeeded, want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				for _, c := range closers {
					c.Close()
				}
			}()
			var got strings.Builder
			for _, r := range readers {
				if _, err := io.Copy(&got, r); err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != tt.want {
				t.Errorf("Open() read %q, want %q", got.String(), tt.want)
			}
		})
	}
}

func TestOpenNoArgsReadsStdin(t *testing.T) {
	readers, closers, err := Open(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(closers) != 0 {
		t.Errorf("Open() returned %d closers for stdin, want 0", len(closers))
	}
	if len(readers) != 1 || readers[0] != io.Reader(os.Stdin) {
		t.Errorf("Open() = %v, want [os.Stdin]", readers)
	}
}
