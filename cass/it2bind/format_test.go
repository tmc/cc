package it2bind

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
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

		// The older store, which is not this format: pretty-printed payloads
		// under a .jsonl name, so no single line is a record. A line reader
		// recovers nothing, and must say so rather than list the path and
		// return "no stream" — see testdata/format/README.md.
		{file: "legacy-pretty.jsonl", want: 0},
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
			// The writer stamps _it2.ts with milliseconds. Assert it is read,
			// because a reader that misses it does not fail — it substitutes
			// something plausible, and the substitution is invisible.
			if got := events[0].Ts(); got == "" {
				t.Error("Ts() is empty: _it2.ts was not read")
			} else if _, err := time.Parse("2006-01-02T15:04:05.000Z", got); err != nil {
				t.Errorf("Ts() = %q does not parse as the writer's layout: %v", got, err)
			}
		})
	}
}

// TestLegacyStoreIsHiddenByFraming is the discrimination control for the
// legacy-pretty.jsonl case above. "Zero records" is a claim two very different
// files satisfy — an empty one, and a full one whose framing a line reader
// cannot see. Asserting only the zero would pass either way, so this asserts
// the other half: the records are really in there, and only the framing hides
// them. Without this, a reader could be pointed at an empty file and the
// corpus would call it conformant.
func TestLegacyStoreIsHiddenByFraming(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "format", "legacy-pretty.jsonl"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	// A streaming decoder reads concatenated JSON values, which is what the
	// old writer produced when it pretty-printed each payload.
	dec := json.NewDecoder(f)
	var got []struct {
		SessionID string `json:"session_id"`
	}
	for {
		var rec struct {
			SessionID string `json:"session_id"`
		}
		if err := dec.Decode(&rec); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("decode: %v", err)
		}
		got = append(got, rec)
	}

	if len(got) != 2 {
		t.Fatalf("concatenated decode = %d records, want 2: the fixture must hold real data, or the zero above proves nothing", len(got))
	}
	const want = "00000001-1200-4000-8000-000000000001"
	for i, rec := range got {
		if rec.SessionID != want {
			t.Errorf("record %d session_id = %q, want %q", i, rec.SessionID, want)
		}
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
