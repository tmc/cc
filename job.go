package cc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Job is a daemon-backed background run stored under ~/.claude/jobs/<shortId>/.
// A job wraps a Claude Code session: shortId is the first 8 chars of SessionID,
// and the daemon writes state.json plus an append-only timeline.jsonl.
type Job struct {
	ShortID         string         `json:"shortId"`
	State           string         `json:"state"`
	Detail          string         `json:"detail,omitempty"`
	Tempo           string         `json:"tempo,omitempty"`
	InFlight        JobInFlight    `json:"inFlight"`
	Output          map[string]any `json:"output,omitempty"`
	Children        []string       `json:"children,omitempty"`
	LinkScanOffset  int64          `json:"linkScanOffset,omitempty"`
	LinkScanPath    string         `json:"linkScanPath,omitempty"`
	Template        string         `json:"template,omitempty"`
	RespawnFlags    []string       `json:"respawnFlags,omitempty"`
	Intent          string         `json:"intent,omitempty"`
	SessionID       string         `json:"sessionId,omitempty"`
	ResumeSessionID string         `json:"resumeSessionId,omitempty"`
	DaemonShort     string         `json:"daemonShort,omitempty"`
	CLIVersion      string         `json:"cliVersion,omitempty"`
	CWD             string         `json:"cwd,omitempty"`
	CreatedAt       time.Time      `json:"createdAt,omitempty"`
	UpdatedAt       time.Time      `json:"updatedAt,omitempty"`
	FirstTerminalAt time.Time      `json:"firstTerminalAt,omitempty"`
	OriginCWD       string         `json:"originCwd,omitempty"`
	Backend         string         `json:"backend,omitempty"`
	Name            string         `json:"name,omitempty"`
	NameSource      string         `json:"nameSource,omitempty"`
}

// JobInFlight is the in-flight counter block inside state.json.
type JobInFlight struct {
	Tasks  int      `json:"tasks"`
	Queued int      `json:"queued"`
	Kinds  []string `json:"kinds,omitempty"`
}

// JobTimelineEvent is one append-only line from a job's timeline.jsonl.
type JobTimelineEvent struct {
	At     time.Time `json:"at"`
	State  string    `json:"state,omitempty"`
	Detail string    `json:"detail,omitempty"`
	Text   string    `json:"text,omitempty"`
}

// JobsDir returns the directory holding daemon job state.
// If CC_JOBS_DIR is set it is used; otherwise ~/.claude/jobs.
func JobsDir() (string, error) {
	if dir := os.Getenv("CC_JOBS_DIR"); dir != "" {
		return dir, nil
	}
	ch, err := ClaudeHome()
	if err != nil {
		return "", fmt.Errorf("jobs dir: %w", err)
	}
	return filepath.Join(ch, "jobs"), nil
}

// ReadJob loads a job by short ID from the default jobs directory.
func ReadJob(shortID string) (*Job, error) {
	dir, err := JobsDir()
	if err != nil {
		return nil, err
	}
	return ReadJobFrom(filepath.Join(dir, shortID))
}

// ReadJobFrom loads a job from a specific directory.
func ReadJobFrom(jobDir string) (*Job, error) {
	data, err := os.ReadFile(filepath.Join(jobDir, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("read job state: %w", err)
	}
	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("parse job state %s: %w", jobDir, err)
	}
	if j.ShortID == "" {
		j.ShortID = filepath.Base(jobDir)
	}
	return &j, nil
}

// ListJobs returns all jobs in the default jobs directory, sorted by short ID.
// Non-job entries (files like pins.json) are skipped silently.
func ListJobs() ([]*Job, error) {
	dir, err := JobsDir()
	if err != nil {
		return nil, err
	}
	return ListJobsIn(dir)
}

// ListJobsIn returns all jobs under the given directory.
func ListJobsIn(dir string) ([]*Job, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	var jobs []*Job
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		j, err := ReadJobFrom(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, k int) bool { return jobs[i].ShortID < jobs[k].ShortID })
	return jobs, nil
}

// ReadJobTimeline reads all events from a job's timeline.jsonl in order.
func ReadJobTimeline(shortID string) ([]JobTimelineEvent, error) {
	dir, err := JobsDir()
	if err != nil {
		return nil, err
	}
	return ReadJobTimelineFrom(filepath.Join(dir, shortID))
}

// ReadJobTimelineFrom reads timeline events from a specific job directory.
func ReadJobTimelineFrom(jobDir string) ([]JobTimelineEvent, error) {
	f, err := os.Open(filepath.Join(jobDir, "timeline.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open timeline: %w", err)
	}
	defer f.Close()
	var events []JobTimelineEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev JobTimelineEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}
