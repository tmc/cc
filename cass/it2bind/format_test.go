package it2bind

import (
	"path/filepath"
	"testing"
)

// TestFormatFixtures runs the shared conformance corpus in
// testdata/format. The corpus exists because more than one implementation
// reads this on-disk format — this package for ccresume, and it2's
// internal/agentbind for "it2 session find --conversation" — in separate
// repositories that deliberately do not import each other. Agreement is
// maintained by both asserting these outcomes, so any change here is a change
// to the format itself and not to one reader.
//
// See testdata/format/README.md for what each case is for.
func TestFormatFixtures(t *testing.T) {
	tests := []struct {
		file     string
		want     int    // records surviving ReadEvents
		wantSID  string // canonical it2 session id of the first survivor
		wantHarn string // harness session id of the first survivor
	}{
		{file: "binding.ndjson", want: 1,
			wantSID:  "00000003-1200-4000-8000-000000000001",
			wantHarn: "00000001-1200-4000-8000-000000000001"},

		// The wNtNpN: prefix is a pane position, not an identity. A reader that
		// keeps it splits one conversation across two directories.
		{file: "prefixed.ndjson", want: 1,
			wantSID:  "00000003-1200-4000-8000-000000000001",
			wantHarn: "00000001-1200-4000-8000-000000000001"},

		// Canonicalization strips the prefix and must not touch case: two
		// spellings of one id must not become two ids.
		{file: "lowercase.ndjson", want: 1,
			wantSID:  "00000003-1200-4000-8000-000000000001",
			wantHarn: "00000001-1200-4000-8000-000000000001"},

		// A writer predating the _it2 enrichment. Real records, unusable.
		{file: "unbound.ndjson", want: 0},

		// The {raw, _it2} wrapper for a non-object payload: has a pane, has no
		// session, cannot be grouped.
		{file: "wrapped.ndjson", want: 0},

		// A blank line and a truncated one. Skipped without failing the file:
		// one torn record must not discard the good records around it.
		{file: "malformed.ndjson", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			events, err := ReadEvents(filepath.Join("testdata", "format", tt.file))
			if err != nil {
				t.Fatalf("ReadEvents: %v", err)
			}
			if len(events) != tt.want {
				t.Fatalf("records = %d, want %d", len(events), tt.want)
			}
			if tt.want == 0 {
				return
			}
			if got := events[0].It2SID(); got != tt.wantSID {
				t.Errorf("It2SID() = %q, want %q", got, tt.wantSID)
			}
			if got := events[0].SessionID; got != tt.wantHarn {
				t.Errorf("SessionID = %q, want %q", got, tt.wantHarn)
			}
		})
	}
}

// TestFormatFixturesBindOneConversation checks the property the format exists
// for: a session that starts in one pane and resumes in another is one harness
// session bound to two panes over time, not two sessions.
func TestFormatFixturesBindOneConversation(t *testing.T) {
	var events []Event
	for _, f := range []string{"binding.ndjson", "prefixed.ndjson"} {
		got, err := ReadEvents(filepath.Join("testdata", "format", f))
		if err != nil {
			t.Fatalf("ReadEvents(%s): %v", f, err)
		}
		events = append(events, got...)
	}
	intervals := Intervals(events)
	if len(intervals) != 1 {
		t.Fatalf("intervals = %d, want 1: the prefixed record names the same pane as the bare one", len(intervals))
	}
}
