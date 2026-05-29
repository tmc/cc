//go:build realindex

// Build with: go test -tags realindex -run TestRealIndexSizes -v ./cass/store/
// This test reads actual Claude Code session files from ~/.claude/projects/
// and measures index sizes for each backend.

package store_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/collector"
	cassstore "github.com/tmc/cc/cass/store"
)

func TestRealIndexSizes(t *testing.T) {
	// Collect real sessions via the ClaudeCode collector.
	col := &collector.ClaudeCode{}
	dr, err := col.Detect(context.Background())
	if err != nil || !dr.Found {
		t.Skip("Claude Code sessions not found:", err)
	}

	out := make(chan cass.Session, 100)
	var sessions []cass.Session
	scanErr := make(chan error, 1)
	go func() {
		scanErr <- col.Scan(context.Background(), cass.ScanConfig{}, out)
	}()
	for sess := range out {
		sessions = append(sessions, sess)
	}
	if err := <-scanErr; err != nil {
		t.Fatal("scan:", err)
	}

	var rawBytes int64
	for _, s := range sessions {
		for _, m := range s.Messages {
			rawBytes += int64(len(m.Content))
		}
	}
	t.Logf("Collected %d real sessions, %.1f MB raw conversation text",
		len(sessions), float64(rawBytes)/1e6)

	for _, tc := range []struct {
		kind   cassstore.BackendKind
		maxFTS int
		name   string
	}{
		{cassstore.BackendSQLite, 0, "sqlite/unicode61 uncapped"},
		{cassstore.BackendSQLite, 32 * 1024, "sqlite/unicode61 32KB cap (current)"},
		{cassstore.BackendSQLitePorter, 0, "sqlite/porter uncapped"},
		{cassstore.BackendSQLitePorter, 32 * 1024, "sqlite/porter 32KB cap"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "index.db")

		st, err := cassstore.NewWithConfig(cassstore.BackendConfig{Kind: tc.kind, Path: path, MaxFTSBytes: tc.maxFTS})
		if err != nil {
			t.Errorf("%s: open: %v", tc.name, err)
			continue
		}

		t0 := time.Now()
		// Index in batches of 50.
		for i := 0; i < len(sessions); i += 50 {
			end := i + 50
			if end > len(sessions) {
				end = len(sessions)
			}
			if err := st.BatchIndex(context.Background(), sessions[i:end]); err != nil {
				log.Printf("%s: batch index: %v", tc.name, err)
				break
			}
		}
		elapsed := time.Since(t0)
		st.Close()

		// Measure on-disk sizes.
		var totalBytes int64
		filepath.Walk(dir, func(_ string, fi os.FileInfo, _ error) error {
			if fi != nil && !fi.IsDir() {
				totalBytes += fi.Size()
			}
			return nil
		})

		// Reopen for FTS shadow stats.
		var ftsShadowKB int64
		st2, _ := cassstore.NewWithConfig(cassstore.BackendConfig{Kind: tc.kind, Path: path})
		if st2 != nil {
			stats, _ := st2.BackendStats(context.Background())
			ftsShadowKB = stats.IndexSizeBytes / 1024
			st2.Close()
		}

		t.Logf("%-45s  total=%6.1f MB  ratio=%5.1fx  fts_shadow=%6d KB  time=%s",
			tc.name,
			float64(totalBytes)/1e6,
			float64(totalBytes)/float64(rawBytes),
			ftsShadowKB,
			elapsed.Round(time.Millisecond),
		)

		fmt.Printf("%-45s  total=%6.1f MB  ratio=%5.1fx  fts_shadow=%6d KB  time=%s\n",
			tc.name,
			float64(totalBytes)/1e6,
			float64(totalBytes)/float64(rawBytes),
			ftsShadowKB,
			elapsed.Round(time.Millisecond),
		)
	}
}
