package it2bind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalSID(t *testing.T) {
	tests := []struct{ in, want string }{
		{"w0t0p4:32000319-77E2-4707-AB2C-F61059938C46", "32000319-77E2-4707-AB2C-F61059938C46"},
		{"w12t3p45:ABCDEF01-2345-6789-ABCD-EF0123456789", "ABCDEF01-2345-6789-ABCD-EF0123456789"},
		{"32000319-77E2-4707-AB2C-F61059938C46", "32000319-77E2-4707-AB2C-F61059938C46"},
		{"--help", "--help"},
		{"test-session-id", "test-session-id"},
	}
	for _, tt := range tests {
		if got := CanonicalSID(tt.in); got != tt.want {
			t.Errorf("CanonicalSID(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsSessionDir(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"32000319-77E2-4707-AB2C-F61059938C46", true},
		{"w0t0p4:32000319-77E2-4707-AB2C-F61059938C46", true}, // prefixed collapses to a UUID.
		{"--help", false},
		{"test-env-session", false},
		{"test-session-id", false},
		{"test-manual-session", false},
		{"not-a-uuid", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isSessionDir(tt.name); got != tt.want {
			t.Errorf("isSessionDir(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// writeEvents creates root/<dir>/artifacts/claude-code-events.ndjson with the
// given raw lines.
func writeEvents(t *testing.T, root, dir string, lines ...string) {
	t.Helper()
	d := filepath.Join(root, dir, "artifacts")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(d, "claude-code-events.ndjson"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// startup builds a real-shape SessionStart line with the _it2 enrichment.
func startup(sessionID, source, it2sid, bootID, ts string) string {
	return `{"session_id":"` + sessionID + `","source":"` + source +
		`","hook_event_name":"SessionStart","transcript_path":"/p/` + sessionID +
		`.jsonl","cwd":"/work","_it2":{"it2_sid":"` + it2sid + `","boot_id":"` + bootID + `","ts":"` + ts + `"}}`
}

func TestDiscoverDirs(t *testing.T) {
	root := t.TempDir()
	// One pane with BOTH a prefixed and a bare dir (the live fragmentation bug).
	writeEvents(t, root, "w0t0p1:AAAAAAAA-1111-2222-3333-444444444444", "{}")
	writeEvents(t, root, "AAAAAAAA-1111-2222-3333-444444444444", "{}")
	// A distinct bare pane.
	writeEvents(t, root, "BBBBBBBB-1111-2222-3333-444444444444", "{}")
	// Junk that must be skipped.
	writeEvents(t, root, "--help", "{}")
	writeEvents(t, root, "test-session-id", "{}")

	dirs, err := DiscoverDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 {
		t.Fatalf("got %d canonical SIDs, want 2: %v", len(dirs), keys(dirs))
	}
	// The prefixed + bare dirs for pane A must collapse to one canonical SID
	// with two on-disk paths.
	a := "AAAAAAAA-1111-2222-3333-444444444444"
	if len(dirs[a]) != 2 {
		t.Errorf("pane A: got %d dirs, want 2 (prefixed + bare): %v", len(dirs[a]), dirs[a])
	}
}

func keys(m map[string][]string) []string {
	var k []string
	for s := range m {
		k = append(k, s)
	}
	return k
}

func TestReadEvents(t *testing.T) {
	root := t.TempDir()
	dir := "AAAAAAAA-1111-2222-3333-444444444444"
	writeEvents(t, root, dir,
		startup("sess-1", "startup", "w0t0p1:AAAAAAAA-1111-2222-3333-444444444444", "boot-x", "2026-07-19T10:00:00Z"),
		``,           // blank line skipped.
		`{not json}`, // malformed skipped.
		`{"_it2":{"it2_sid":"AAAAAAAA-1111-2222-3333-444444444444","boot_id":"boot-x","ts":"t"}}`, // no session_id → dropped.
		`{"session_id":"sess-2","_it2":{"it2_sid":"x","ts":"t"}}`,                                 // no boot_id → dropped.
		startup("sess-1", "resume", "AAAAAAAA-1111-2222-3333-444444444444", "boot-x", "2026-07-19T11:00:00Z"),
	)
	evs, err := ReadEvents(filepath.Join(root, dir, "artifacts", "claude-code-events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(evs), evs)
	}
	// It2SID must be canonicalized (prefix stripped).
	if got := evs[0].It2SID(); got != "AAAAAAAA-1111-2222-3333-444444444444" {
		t.Errorf("event 0 It2SID = %q, want canonical bare UUID", got)
	}
	if evs[0].Source != "startup" || evs[1].Source != "resume" {
		t.Errorf("sources = %q, %q; want startup, resume", evs[0].Source, evs[1].Source)
	}
}

// TestResumeInDifferentPane is the load-bearing case: one harness session bound
// to two different panes over time within one boot → two intervals, latest wins.
func TestResumeInDifferentPane(t *testing.T) {
	events := []Event{
		mustEvent(startup("sess-1", "startup", "PANE-A", "boot-1", "2026-07-19T10:00:00Z")),
		mustEvent(startup("sess-1", "resume", "PANE-A", "boot-1", "2026-07-19T10:05:00Z")),
		mustEvent(startup("sess-1", "resume", "PANE-B", "boot-1", "2026-07-19T12:00:00Z")),
	}
	ivs := Intervals(events)
	// Same (session_id, boot_id) → grouped into ONE interval; latest event's
	// pane wins as the current binding.
	if len(ivs) != 1 {
		t.Fatalf("got %d intervals, want 1: %+v", len(ivs), ivs)
	}
	if ivs[0].It2SID != "PANE-B" {
		t.Errorf("interval It2SID = %q, want PANE-B (latest)", ivs[0].It2SID)
	}
	if ivs[0].Start != "2026-07-19T10:00:00Z" || ivs[0].End != "2026-07-19T12:00:00Z" {
		t.Errorf("interval span = [%s, %s]", ivs[0].Start, ivs[0].End)
	}

	sid, ok := CurrentSID(ivs, "sess-1", "boot-1")
	if !ok || sid != "PANE-B" {
		t.Errorf("CurrentSID = %q, %v; want PANE-B, true", sid, ok)
	}
	// A different boot has no binding — prior SIDs are dead cross-boot.
	if _, ok := CurrentSID(ivs, "sess-1", "boot-2"); ok {
		t.Error("CurrentSID for a different boot should report no binding")
	}
}

// TestBootPartition: same session, same pane, DIFFERENT boots → separate
// intervals (it2 SIDs are only comparable within a boot).
func TestBootPartition(t *testing.T) {
	events := []Event{
		mustEvent(startup("sess-1", "startup", "PANE-A", "boot-1", "2026-07-19T10:00:00Z")),
		mustEvent(startup("sess-1", "resume", "PANE-A", "boot-2", "2026-07-20T10:00:00Z")),
	}
	ivs := Intervals(events)
	if len(ivs) != 2 {
		t.Fatalf("got %d intervals, want 2 (one per boot): %+v", len(ivs), ivs)
	}
	// Sorted by start: boot-1 first.
	if ivs[0].BootID != "boot-1" || ivs[1].BootID != "boot-2" {
		t.Errorf("boots = %q, %q; want boot-1, boot-2", ivs[0].BootID, ivs[1].BootID)
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	const paneA = "AAAAAAAA-1111-2222-3333-444444444444"
	// Pane A fragmented across prefixed + bare dirs, both feeding sess-1.
	writeEvents(t, root, "w0t0p1:"+paneA,
		startup("sess-1", "startup", paneA, "boot-1", "2026-07-19T10:00:00Z"))
	writeEvents(t, root, paneA,
		startup("sess-1", "resume", paneA, "boot-1", "2026-07-19T11:00:00Z"))
	// Junk dir with events must be ignored.
	writeEvents(t, root, "--help", startup("junk", "startup", "x", "b", "t"))

	ivs, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ivs) != 1 {
		t.Fatalf("got %d intervals, want 1 (fragmented dirs merged, junk skipped): %+v", len(ivs), ivs)
	}
	if len(ivs[0].Events) != 2 {
		t.Errorf("merged interval should hold both events, got %d", len(ivs[0].Events))
	}
}

// TestSessionsForSID: a pane that hosted several sessions over time yields them
// most-recently-bound first, deduped, and matches through the wNtNpN: prefix.
func TestSessionsForSID(t *testing.T) {
	events := []Event{
		mustEvent(startup("sess-old", "startup", "PANE-A", "boot-1", "2026-07-19T09:00:00Z")),
		mustEvent(startup("sess-new", "startup", "PANE-A", "boot-1", "2026-07-19T12:00:00Z")),
		mustEvent(startup("sess-new", "resume", "PANE-A", "boot-1", "2026-07-19T13:00:00Z")),
		mustEvent(startup("sess-other", "startup", "PANE-B", "boot-1", "2026-07-19T10:00:00Z")),
	}
	ivs := Intervals(events)

	// Query with the wNtNpN: prefix must still match pane A.
	got := SessionsForSID(ivs, "w0t0p1:PANE-A")
	want := []string{"sess-new", "sess-old"} // most-recently-bound first.
	if len(got) != len(want) {
		t.Fatalf("SessionsForSID = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SessionsForSID[%d] = %q, want %q (order: recent first)", i, got[i], want[i])
		}
	}

	if got := SessionsForSID(ivs, "PANE-NONE"); got != nil {
		t.Errorf("SessionsForSID for unknown pane = %v, want nil", got)
	}
}

func mustEvent(line string) Event {
	e, err := parseEventLine(line)
	if err != nil {
		panic(err)
	}
	return e
}
