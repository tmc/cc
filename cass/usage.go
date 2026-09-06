package cass

import "github.com/tmc/cc/ccusage"

// UsageEvent is one model request as the index stores it: the normalized token
// accounting [ccusage.FromEntries] produces, plus the repository it was
// attributed to. GitCommonDir is resolved once per session at index time and
// stamped on every one of its events; it is empty when nothing could be
// resolved, which is a reported bucket rather than a dropped row.
type UsageEvent struct {
	ccusage.Record
	GitCommonDir string
	// Attribution names the step that set GitCommonDir: "git" from the
	// session's own git context, "path" by resolving a working directory,
	// "prefix" from a known worktree path the directory sits under, or "" for
	// none. Reports break the coverage down by it.
	Attribution string
}

// UsageEvents wraps normalized records as unattributed usage events.
func UsageEvents(records []ccusage.Record) []UsageEvent {
	if len(records) == 0 {
		return nil
	}
	events := make([]UsageEvent, len(records))
	for i, r := range records {
		events[i] = UsageEvent{Record: r}
	}
	return events
}
