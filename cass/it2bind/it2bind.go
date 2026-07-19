package it2bind

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// eventsFile is the per-pane NDJSON file the SessionStart hook appends to,
// relative to a session directory.
const eventsFile = "artifacts/claude-code-events.ndjson"

// prefixPattern matches the wNtNpN: prefix that $ITERM_SESSION_ID carries. The
// hook writer strips it, but older data (and the pre-fix writer) wrote it into
// the directory name, so the reader canonicalizes defensively.
var prefixPattern = regexp.MustCompile(`^w\d+t\d+p\d+:`)

// uuidPattern matches a canonical iTerm2 session ID (a UUID).
var uuidPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// Enrichment is the _it2 sub-object the writer stamps onto every line. The
// harness supplies the rest of the payload; the writer never rewrites it.
type Enrichment struct {
	It2SID string `json:"it2_sid"`
	BootID string `json:"boot_id"`
	Ts     string `json:"ts"`
}

// Event is one binding record appended by the SessionStart hook. The raw hook
// payload provides SessionID, Source, HookEvent, TranscriptPath, and Cwd at the
// top level; the writer adds the [Enrichment] under the _it2 key. Non-object
// hook payloads are wrapped by the writer as {raw, _it2}; those lack a
// SessionID and are dropped by [ReadEvents] since they cannot be grouped.
type Event struct {
	SessionID      string     `json:"session_id"`
	Source         string     `json:"source"`          // "startup" or "resume"
	HookEvent      string     `json:"hook_event_name"` // e.g. "SessionStart"
	TranscriptPath string     `json:"transcript_path"`
	Cwd            string     `json:"cwd"`
	It2            Enrichment `json:"_it2"`
}

// It2SID returns the canonical (bare) iTerm2 session ID for the event.
func (e Event) It2SID() string { return CanonicalSID(e.It2.It2SID) }

// BootID returns the boot session UUID stamped by the writer.
func (e Event) BootID() string { return e.It2.BootID }

// Ts returns the writer-stamped ISO-8601 timestamp.
func (e Event) Ts() string { return e.It2.Ts }

// Interval is a contiguous binding of one harness session to one iTerm2 pane
// within a single boot. Start is the first event's timestamp; End is the last
// event's timestamp in the same (SessionID, BootID) group (open if the session
// is still bound to this pane).
type Interval struct {
	SessionID string
	It2SID    string // canonical (bare UUID)
	BootID    string
	Cwd       string
	Start     string
	End       string
	Events    []Event
}

// CanonicalSID strips the wNtNpN: prefix from an iTerm2 session ID or directory
// name, yielding the bare UUID. A name without the prefix is returned unchanged.
func CanonicalSID(name string) string {
	return prefixPattern.ReplaceAllString(name, "")
}

// isSessionDir reports whether a directory entry name is a real per-pane
// session directory. It rejects junk (--help, test-*) and non-UUID names, after
// canonicalizing the wNtNpN: prefix.
func isSessionDir(name string) bool {
	return uuidPattern.MatchString(CanonicalSID(name))
}

// DiscoverDirs returns the canonicalized set of iTerm2 session directories under
// root (typically ~/.it2/sessions), skipping junk and non-UUID names. Prefixed
// and bare directory names for the same pane collapse to one canonical SID; the
// returned map values are the on-disk directory paths that share it.
func DiscoverDirs(root string) (map[string][]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	dirs := make(map[string][]string)
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !isSessionDir(name) {
			continue
		}
		sid := CanonicalSID(name)
		dirs[sid] = append(dirs[sid], filepath.Join(root, name))
	}
	return dirs, nil
}

// ReadEvents parses the binding events from a single per-pane events file.
// Malformed lines are skipped. Each event's It2SID is canonicalized. Events
// missing a SessionID or BootID are dropped, since they cannot be grouped.
func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		e, err := parseEventLine(sc.Text())
		if err != nil {
			continue // blank, malformed, or ungroupable line: skip.
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return events, err
	}
	return events, nil
}

// parseEventLine parses one NDJSON binding line, canonicalizing the it2 session
// ID. It returns an error for blank, malformed, or ungroupable lines — those
// missing a session id or boot id (including the writer's {raw, _it2} wrapper
// for non-object hook payloads).
func parseEventLine(line string) (Event, error) {
	var e Event
	line = strings.TrimSpace(line)
	if line == "" {
		return e, errSkip
	}
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return e, err
	}
	if e.SessionID == "" || e.BootID() == "" {
		return e, errSkip
	}
	e.It2.It2SID = CanonicalSID(e.It2.It2SID)
	return e, nil
}

// errSkip marks a line that parsed but cannot be grouped into an interval.
var errSkip = errString("skip: ungroupable binding line")

type errString string

func (e errString) Error() string { return string(e) }

// Intervals folds a set of binding events into per-(session_id, boot_id)
// intervals, sorted by start time. Events within a group are ordered by
// timestamp; Start and End bracket the group.
func Intervals(events []Event) []Interval {
	type key struct{ session, boot string }
	groups := make(map[key][]Event)
	for _, e := range events {
		k := key{e.SessionID, e.BootID()}
		groups[k] = append(groups[k], e)
	}

	var out []Interval
	for k, evs := range groups {
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Ts() < evs[j].Ts() })
		iv := Interval{
			SessionID: k.session,
			BootID:    k.boot,
			Events:    evs,
			Start:     evs[0].Ts(),
			End:       evs[len(evs)-1].Ts(),
			It2SID:    evs[len(evs)-1].It2SID(),
			Cwd:       evs[0].Cwd,
		}
		out = append(out, iv)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

// Load discovers, reads, and folds all binding events under root into
// intervals. It is the convenience entry point over DiscoverDirs → ReadEvents →
// Intervals. Directories without an events file are skipped.
func Load(root string) ([]Interval, error) {
	dirs, err := DiscoverDirs(root)
	if err != nil {
		return nil, err
	}
	var all []Event
	for _, paths := range dirs {
		for _, dir := range paths {
			evs, err := ReadEvents(filepath.Join(dir, eventsFile))
			if err != nil {
				continue // missing or unreadable events file: skip.
			}
			all = append(all, evs...)
		}
	}
	return Intervals(all), nil
}

// CurrentSID returns the iTerm2 session ID currently bound to the given harness
// session within the given boot: the It2SID of the latest interval for that
// (session_id, boot_id). It reports false if no binding exists. Callers on a
// different boot should treat all prior bindings as dead.
func CurrentSID(intervals []Interval, sessionID, bootID string) (string, bool) {
	var latest *Interval
	for i := range intervals {
		iv := &intervals[i]
		if iv.SessionID != sessionID || iv.BootID != bootID {
			continue
		}
		if latest == nil || iv.End > latest.End {
			latest = iv
		}
	}
	if latest == nil {
		return "", false
	}
	return latest.It2SID, true
}
