// Package sessionargs opens the session transcripts a command was asked for.
//
// The cc commands that read transcripts share one convention: the positional
// arguments name session files, no arguments means standard input, and -since
// stands in for the arguments by naming every session modified within a
// window. Open implements that convention once.
package sessionargs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/tmc/cc"
	"github.com/tmc/cc/ccpaths"
)

// Open returns a reader for each session file named in args, along with the
// closers the caller is responsible for closing.
//
// With no args Open reads standard input, unless since is non-empty: then it
// reads every session file modified within that window, in the syntax
// [ccpaths.ParseDuration] accepts. Commands without a -since flag pass "".
//
// A named file that cannot be opened is reported on standard error and
// skipped, so one unreadable file does not end a run over a whole window.
// Open fails only when nothing could be opened at all.
func Open(ctx context.Context, args []string, since string) ([]io.Reader, []io.Closer, error) {
	if since != "" && len(args) == 0 {
		d, err := ccpaths.ParseDuration(since)
		if err != nil {
			return nil, nil, err
		}
		files, err := cc.FindSessionFiles(ctx, d, "")
		if err != nil {
			return nil, nil, err
		}
		args = files
	}
	if len(args) == 0 {
		return []io.Reader{os.Stdin}, nil, nil
	}
	var readers []io.Reader
	var closers []io.Closer
	for _, path := range args {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}
	if len(readers) == 0 {
		return nil, nil, errors.New("no readable session files")
	}
	return readers, closers, nil
}
