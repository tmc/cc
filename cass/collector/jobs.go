package collector

import (
	"fmt"

	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccjobstore"
)

// ScanJobs reads ~/.claude/jobs/<shortId>/state.json + timeline.jsonl files
// and returns store-shaped Job records. Pass an empty root to use the default.
func ScanJobs(root string) ([]store.Job, error) {
	jobs, err := listJobs(root)
	if err != nil {
		return nil, err
	}
	out := make([]store.Job, 0, len(jobs))
	for _, j := range jobs {
		events, _ := readTimelineFor(root, j.ShortID)
		out = append(out, toStoreJob(j, events))
	}
	return out, nil
}

func listJobs(root string) ([]*ccjobstore.Job, error) {
	if root == "" {
		return ccjobstore.ListJobs()
	}
	return ccjobstore.ListJobsIn(root)
}

func readTimelineFor(root, shortID string) ([]ccjobstore.JobTimelineEvent, error) {
	if root == "" {
		return ccjobstore.ReadJobTimeline(shortID)
	}
	return ccjobstore.ReadJobTimelineFrom(joinJob(root, shortID))
}

func joinJob(root, shortID string) string { return root + "/" + shortID }

func toStoreJob(j *ccjobstore.Job, events []ccjobstore.JobTimelineEvent) store.Job {
	if j == nil {
		return store.Job{}
	}
	return store.Job{
		ShortID:         j.ShortID,
		SessionID:       j.SessionID,
		ResumeSessionID: j.ResumeSessionID,
		Name:            j.Name,
		NameSource:      j.NameSource,
		Intent:          j.Intent,
		State:           j.State,
		Detail:          j.Detail,
		Tempo:           j.Tempo,
		InFlightTasks:   j.InFlight.Tasks,
		InFlightQueued:  j.InFlight.Queued,
		Template:        j.Template,
		Backend:         j.Backend,
		CLIVersion:      j.CLIVersion,
		CWD:             j.CWD,
		OriginCWD:       j.OriginCWD,
		LinkScanPath:    j.LinkScanPath,
		LinkScanOffset:  j.LinkScanOffset,
		CreatedAt:       unixOrZero(j.CreatedAt.Unix(), j.CreatedAt.IsZero()),
		UpdatedAt:       unixOrZero(j.UpdatedAt.Unix(), j.UpdatedAt.IsZero()),
		FirstTerminalAt: unixOrZero(j.FirstTerminalAt.Unix(), j.FirstTerminalAt.IsZero()),
		EventCount:      len(events),
		OutputResult:    extractOutputResult(j.Output),
		SourcePath:      fmt.Sprintf("jobs/%s", j.ShortID),
	}
}

func unixOrZero(u int64, zero bool) int64 {
	if zero {
		return 0
	}
	return u
}

func extractOutputResult(out map[string]any) string {
	if out == nil {
		return ""
	}
	if v, ok := out["result"].(string); ok {
		return v
	}
	return ""
}
