package collector

import (
	"context"

	"github.com/tmc/cc"
	"github.com/tmc/cc/cass"
)

// readSessionFile reads a session or subagent JSONL file, using the injected
// ScanConfig.Parse hook when set (so a long-lived server can supply a
// cache-backed incremental reader) and falling back to cc.ReadFile otherwise.
// The result is identical either way, so collector behavior is unchanged when
// Parse is nil.
func readSessionFile(ctx context.Context, config cass.ScanConfig, path string) ([]cc.Entry, error) {
	if config.Parse != nil {
		return config.Parse(ctx, path)
	}
	return cc.ReadFile(ctx, path)
}
