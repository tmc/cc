package cc

import (
	"bytes"
	"context"
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
			entries, err := ReadFile(context.Background(), path)
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

			got, err := ReadAll(context.Background(), bytes.NewReader(buf.Bytes()))
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

func TestEntryLinkFieldsRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
	}{
		{"origin", `{"type":"user","origin":"human"}`},
		{"bridge", `{"type":"bridge-session","bridgeSessionId":"cse_123"}`},
		{"pull request", `{"type":"pr-link","prNumber":42,"prUrl":"https://example.com/pr/42","prRepository":"tmc/cc"}`},
		{"frame", `{"type":"frame-link","frameUrl":"https://example.com/frame","path":"/work","title":"session"}`},
		{"timestamp", `{"type":"user","timestamp":"2026-09-29T12:00:00Z"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var entry Entry
			if err := json.Unmarshal([]byte(tt.input), &entry); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.input), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip = %s, want %s", data, tt.input)
			}
		})
	}
}
