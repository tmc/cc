package cchooks

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestReadStoreStages checks that each way an event store can yield nothing is
// reported as a different finding. Distinguishing them is the whole point: a
// store nothing writes to, a store written but empty, and a store full of
// records missing the binding field all resolve to zero bindings, and only the
// last one means the writer is stale.
func TestReadStoreStages(t *testing.T) {
	// bound is a record carrying both identifiers; unbound predates _it2.
	const bound = `{"session_id":"s1","hook_event_name":"SessionStart","_it2":{"it2_sid":"00000003-1111-2222-3333-444455556666","ts":"2026-08-17T00:00:00Z"}}`
	const unbound = `{"session_id":"s1","hook_event_name":"SessionStart"}`

	tests := []struct {
		name  string
		lines []string // nil means create no events file at all
		file  bool
		want  string
	}{
		{"never written", nil, false, CodeStoreUnwritten},
		{"written but empty", nil, true, CodeStoreEmpty},
		{"stale writer", []string{unbound, unbound}, true, CodeStoreUnbound},
		{"healthy", []string{bound}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "00000003-1111-2222-3333-444455556666")
			mkdirAll(t, dir)
			if tt.file {
				mkdirAll(t, filepath.Join(dir, "artifacts"))
				var body string
				if len(tt.lines) > 0 {
					body = strings.Join(tt.lines, "\n") + "\n"
				}
				writeFile(t, filepath.Join(dir, eventsFile), body)
			}

			st, diags := ReadStore(root)
			got := codes(diags)
			if tt.want == "" {
				for _, c := range []string{CodeStoreUnwritten, CodeStoreEmpty, CodeStoreUnbound, CodeStoreUnresolved} {
					if got[c] {
						t.Errorf("healthy store reported %q; stats %+v", c, st)
					}
				}
				return
			}
			if !got[tt.want] {
				t.Errorf("want %q, got %v; stats %+v", tt.want, keys(got), st)
			}
			// Only the failing stage is reported, so one empty store does not
			// read as four separate problems.
			if n := len(diags); n != 1 {
				t.Errorf("diagnostics = %d, want 1: %v", n, keys(got))
			}
		})
	}
}

// TestReadStoreAbsent covers a root that does not exist. Nothing installed is
// not a fault, so this must not read as a broken store.
func TestReadStoreAbsent(t *testing.T) {
	_, diags := ReadStore(filepath.Join(t.TempDir(), "nonexistent"))
	if !codes(diags)[CodeStoreAbsent] {
		t.Fatalf("want %q, got %v", CodeStoreAbsent, diags)
	}
	if Worst(diags) != "info" {
		t.Errorf("severity = %q, want info: an absent store is not a fault", Worst(diags))
	}
}

// TestReadStoreTwinned covers the same conversation held under both a bare
// UUID and a wNtNpN:-prefixed directory.
func TestReadStoreTwinned(t *testing.T) {
	root := t.TempDir()
	const sid = "00000003-1111-2222-3333-444455556666"
	for _, name := range []string{sid, "w0t0p2:" + sid} {
		mkdirAll(t, filepath.Join(root, name, "artifacts"))
		writeFile(t, filepath.Join(root, name, eventsFile), "")
	}
	st, diags := ReadStore(root)
	if st.Twinned != 1 {
		t.Errorf("Twinned = %d, want 1", st.Twinned)
	}
	if !codes(diags)[CodeStoreTwinned] {
		t.Errorf("want %q, got %v", CodeStoreTwinned, keys(codes(diags)))
	}
	// Twins are a writer defect, not a reader one, and the advice must not
	// send anyone to merge directories that hold identically named files.
	for _, d := range diags {
		if d.Code == CodeStoreTwinned && strings.Contains(d.Message, "merge the twins") {
			t.Error("twinned advice must not recommend merging: a canonicalizing reader already folds both, and a move destroys events")
		}
	}
}
