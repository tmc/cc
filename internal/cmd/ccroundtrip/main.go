// Command ccroundtrip checks that harness histories survive normalization.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/tmc/cc"
)

type report struct {
	Path            string `json:"path"`
	Harness         string `json:"harness"`
	Entries         int    `json:"entries"`
	ToolUses        int    `json:"tool_uses"`
	ToolResults     int    `json:"tool_results"`
	DanglingUses    int    `json:"dangling_uses"`
	OrphanResults   int    `json:"orphan_results"`
	TruncatedFields int    `json:"truncated_fields,omitempty"`
	Error           string `json:"error,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "ccroundtrip: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ccroundtrip", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOutput := fs.Bool("json", false, "print reports as JSON")
	strict := fs.Bool("strict", false, "fail on dangling tool calls and orphan tool results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: ccroundtrip [-json] [-strict] path ...")
	}

	paths, err := sessionFiles(fs.Args())
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("no session files found")
	}

	reports := make([]report, 0, len(paths))
	failed := false
	for _, path := range paths {
		r := checkFile(ctx, path, *strict)
		if r.Error != "" {
			failed = true
		}
		reports = append(reports, r)
	}
	if *jsonOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			return err
		}
	} else {
		for _, r := range reports {
			if r.Error != "" {
				fmt.Fprintf(stdout, "FAIL %s: %s\n", r.Path, r.Error)
				continue
			}
			fmt.Fprintf(stdout, "ok %s harness=%s entries=%d tools=%d results=%d dangling=%d orphan=%d",
				r.Path, r.Harness, r.Entries, r.ToolUses, r.ToolResults, r.DanglingUses, r.OrphanResults)
			if r.TruncatedFields > 0 {
				fmt.Fprintf(stdout, " truncated=%d", r.TruncatedFields)
			}
			fmt.Fprintln(stdout)
		}
	}
	if failed {
		return errors.New("one or more histories failed")
	}
	return nil
}

func sessionFiles(args []string) ([]string, error) {
	seen := make(map[string]bool)
	var paths []string
	add := func(path string) {
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(arg)
			continue
		}
		err = filepath.WalkDir(arg, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			if filepath.Base(path) == "transcript_full.jsonl" {
				compact := filepath.Join(filepath.Dir(path), "transcript.jsonl")
				if _, err := os.Stat(compact); err == nil {
					return nil
				}
			}
			add(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func checkFile(ctx context.Context, path string, strict bool) report {
	r := report{Path: path, Harness: cc.Harness(path)}
	entries, err := cc.ReadFile(ctx, path)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if len(entries) == 0 {
		r.Error = "decoded no entries"
		return r
	}
	r.Entries = len(entries)

	var normalized bytes.Buffer
	enc := json.NewEncoder(&normalized)
	for i, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			r.Error = fmt.Sprintf("encode entry %d: %v", i, err)
			return r
		}
	}
	got, err := cc.ReadAll(ctx, bytes.NewReader(normalized.Bytes()))
	if err != nil {
		r.Error = fmt.Sprintf("decode normalized history: %v", err)
		return r
	}
	if len(got) != len(entries) {
		r.Error = fmt.Sprintf("normalized entry count = %d, want %d", len(got), len(entries))
		return r
	}
	for i := range entries {
		if !equalNormalized(entries[i], got[i]) {
			r.Error = fmt.Sprintf("normalized entry %d changed", i)
			return r
		}
	}

	uses := make(map[string]int)
	results := make(map[string]int)
	for _, entry := range entries {
		if entry.Message == nil {
			continue
		}
		for _, use := range entry.Message.ToolUses() {
			r.ToolUses++
			uses[use.ID]++
		}
		for _, result := range entry.Message.ToolResults() {
			r.ToolResults++
			results[result.ToolUseID]++
		}
	}
	for id, count := range uses {
		if id == "" || count > 1 {
			r.Error = fmt.Sprintf("tool use ID %q occurs %d times", id, count)
			return r
		}
		if results[id] == 0 {
			r.DanglingUses++
		}
	}
	for id := range results {
		if id == "" || uses[id] == 0 {
			r.OrphanResults++
		}
	}
	if strict && (r.DanglingUses > 0 || r.OrphanResults > 0) {
		r.Error = fmt.Sprintf("tool references: %d dangling uses, %d orphan results", r.DanglingUses, r.OrphanResults)
		return r
	}

	if r.Harness == "antigravity" {
		r.TruncatedFields, err = checkAntigravityCompanion(path)
		if err != nil {
			r.Error = err.Error()
		}
	}
	return r
}

var (
	rawMessageType = reflect.TypeOf(json.RawMessage(nil))
	timeType       = reflect.TypeOf(time.Time{})
)

// equalNormalized compares normalized entries while treating RawMessage
// values as JSON rather than as their original byte spelling. Encoding may
// legitimately rewrite insignificant whitespace or escapes such as "<" and
// "\u003c".
func equalNormalized(a, b cc.Entry) bool {
	return equalValue(reflect.ValueOf(a), reflect.ValueOf(b))
}

func equalValue(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == rawMessageType {
		return equalJSON(a.Bytes(), b.Bytes())
	}

	switch a.Kind() {
	case reflect.Interface, reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equalValue(a.Elem(), b.Elem())
	case reflect.Struct:
		if a.Type() == timeType {
			return a.Interface().(time.Time).Equal(b.Interface().(time.Time))
		}
		for i := 0; i < a.NumField(); i++ {
			if !equalValue(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && (a.IsNil() != b.IsNil()) {
			return false
		}
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !equalValue(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Map:
		if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
			return false
		}
		for _, key := range a.MapKeys() {
			bv := b.MapIndex(key)
			if !bv.IsValid() || !equalValue(a.MapIndex(key), bv) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

func equalJSON(a, b []byte) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(av, bv)
}

type rawStep struct {
	StepIndex       int                        `json:"step_index"`
	Type            string                     `json:"type"`
	CreatedAt       string                     `json:"created_at"`
	TruncatedFields []string                   `json:"truncated_fields"`
	Fields          map[string]json.RawMessage `json:"-"`
}

func (s *rawStep) UnmarshalJSON(data []byte) error {
	type plain rawStep
	if err := json.Unmarshal(data, (*plain)(s)); err != nil {
		return err
	}
	return json.Unmarshal(data, &s.Fields)
}

func checkAntigravityCompanion(path string) (int, error) {
	dir := filepath.Dir(path)
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		dir = filepath.Join(path, ".system_generated", "logs")
	}
	if filepath.Base(path) != "transcript.jsonl" && filepath.Base(path) != "transcript_full.jsonl" {
		return 0, nil
	}
	compactPath := filepath.Join(dir, "transcript.jsonl")
	fullPath := filepath.Join(dir, "transcript_full.jsonl")
	compact, err := readRawSteps(compactPath)
	if filepath.Base(path) == "transcript_full.jsonl" && errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var truncated int
	for _, step := range compact {
		truncated += len(step.TruncatedFields)
	}
	if truncated == 0 {
		return 0, nil
	}
	full, err := readRawSteps(fullPath)
	if err != nil {
		return 0, fmt.Errorf("read AGY full transcript: %w", err)
	}
	queues := make(map[string][]rawStep)
	for _, step := range full {
		key := rawStepKey(step)
		queues[key] = append(queues[key], step)
	}
	for _, step := range compact {
		key := rawStepKey(step)
		matches := queues[key]
		if len(matches) == 0 {
			if len(step.TruncatedFields) > 0 {
				return 0, fmt.Errorf("AGY full transcript has no occurrence for %s", key)
			}
			continue
		}
		queues[key] = matches[1:]
		for _, field := range step.TruncatedFields {
			if _, ok := matches[0].Fields[field]; !ok {
				return 0, fmt.Errorf("AGY full transcript occurrence %s lacks %q", key, field)
			}
		}
	}
	return truncated, nil
}

func rawStepKey(step rawStep) string {
	return fmt.Sprintf("%d:%s:%s", step.StepIndex, step.Type, step.CreatedAt)
}

func readRawSteps(path string) ([]rawStep, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var steps []rawStep
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256<<10), cc.MaxLineSize)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var step rawStep
		if err := json.Unmarshal(scanner.Bytes(), &step); err != nil {
			continue
		}
		steps = append(steps, step)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return steps, nil
}
