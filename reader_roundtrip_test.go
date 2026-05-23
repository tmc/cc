package cc

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSampleSessionsRoundTripNormalizedJSONL(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no sample session jsonl files")
	}

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			entries, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) == 0 {
				t.Fatalf("%s decoded no entries", path)
			}

			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			for i, entry := range entries {
				if err := enc.Encode(entry); err != nil {
					t.Fatalf("encode entry %d: %v", i, err)
				}
			}

			got, err := ReadAll(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode encoded session: %v", err)
			}
			if len(got) != len(entries) {
				t.Fatalf("decoded %d entries after round trip, want %d", len(got), len(entries))
			}
			for i := range entries {
				if !reflect.DeepEqual(got[i], entries[i]) {
					wantJSON, _ := json.Marshal(entries[i])
					gotJSON, _ := json.Marshal(got[i])
					t.Fatalf("entry %d mismatch after round trip\nwant %s\n got %s", i, wantJSON, gotJSON)
				}
			}
		})
	}
}
