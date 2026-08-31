package cc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// ResolveSessionFile maps arg to a session file. An existing path is used
// as-is. Anything else is treated as a session ID (or ID prefix) to look up,
// first in the fast index and then by scanning session filenames modified in
// the last since duration. It returns an error when no session matches or
// when the ID is ambiguous; the ambiguity error lists every match.
func ResolveSessionFile(ctx context.Context, arg string, since time.Duration) (string, error) {
	if _, err := os.Stat(arg); err == nil {
		return arg, nil
	}
	id := strings.ToLower(arg)
	var hits []string
	seen := make(map[string]bool)
	if entries, err := AllIndexEntries(since, ""); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(strings.ToLower(e.SessionID), id) && !seen[e.FullPath] {
				seen[e.FullPath] = true
				hits = append(hits, e.FullPath)
			}
		}
	}
	if len(hits) == 0 {
		files, err := FindSessionFiles(ctx, since, "")
		if err != nil {
			return "", err
		}
		for _, f := range files {
			base := strings.ToLower(strings.TrimSuffix(f[strings.LastIndexByte(f, '/')+1:], ".jsonl"))
			if strings.HasPrefix(base, id) || strings.Contains(base, id) {
				hits = append(hits, f)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no session matches %q in the last %s", arg, since)
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("ambiguous session %q: %d matches:\n  %s",
			arg, len(hits), strings.Join(hits, "\n  "))
	}
}
