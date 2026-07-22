package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunAntigravity(t *testing.T) {
	path := filepath.Join("testdata", "agy")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s\nstdout: %s", err, stderr.String(), stdout.String())
	}
	if got := strings.Count(stdout.String(), "\nok ") + strings.Count(stdout.String(), "ok "); got != 1 {
		t.Fatalf("reported %d histories, want 1:\n%s", got, stdout.String())
	}
	for _, want := range []string{"harness=antigravity", "tools=1", "results=1", "truncated=3"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestRunJSON(t *testing.T) {
	path := filepath.Join("testdata", "agy", ".system_generated", "logs", "transcript.jsonl")
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), []string{"-json", path}, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, stderr.String())
	}
	var reports []report
	if err := json.Unmarshal(stdout.Bytes(), &reports); err != nil {
		t.Fatalf("decode output: %v\n%s", err, stdout.String())
	}
	if len(reports) != 1 || reports[0].Error != "" || reports[0].TruncatedFields != 3 {
		t.Fatalf("reports = %+v", reports)
	}
}

func TestSessionFilesSkipsAntigravityFullCompanion(t *testing.T) {
	paths, err := sessionFiles([]string{filepath.Join("testdata", "agy")})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || filepath.Base(paths[0]) != "transcript.jsonl" {
		t.Fatalf("paths = %v, want compact transcript only", paths)
	}
}
