package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTailEveryBoundary(t *testing.T) {
	for _, name := range []string{"sample-session.jsonl", "codex-compactions.jsonl", "subagent-parent.jsonl"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeTestFile(t, path, "")
			tail, err := NewTail(path)
			if err != nil {
				t.Fatal(err)
			}
			var accumulated []Entry
			for i, b := range data {
				// Poll around both incomplete and completed lines.
				if b != '\n' && i%37 != 0 {
					continue
				}
				writeTestFile(t, path, string(data[:i+1]))
				update, err := tail.Poll(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if update.Replace {
					accumulated = nil
				}
				for _, r := range update.Revisions {
					accumulated[r.Index] = r.Entry
				}
				accumulated = append(accumulated, update.Entries...)
				complete := i + 1
				for complete > 0 && data[complete-1] != '\n' {
					complete--
				}
				full, err := ReadAll(context.Background(), bytes.NewReader(data[:complete]))
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(accumulated, full) {
					a, _ := json.Marshal(accumulated)
					f, _ := json.Marshal(full)
					t.Fatalf("byte %d\ngot %s\nwant %s", i, a, f)
				}
				want := Summarize(tail.path, full)
				if !reflect.DeepEqual(update.Summary, want) {
					t.Fatalf("byte %d summary = %+v, want %+v", i, update.Summary, want)
				}
			}
		})
	}
}

func TestTailRevisionIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTestFile(t, path, `{"type":"assistant","message":{"role":"assistant","content":"first"}}`+"\n")
	tail, _ := NewTail(path)
	first, err := tail.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Return usage for the old message, then start a new assistant in one poll.
	appendTestTail(t, path, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10}}}}`+"\n"+`{"type":"assistant","message":{"role":"assistant","content":"second"}}`+"\n")
	next, err := tail.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Entries[0].Message.Usage != nil {
		t.Fatal("old output mutated")
	}
	if len(next.Revisions) != 1 || next.Revisions[0].Index != 0 || next.Revisions[0].Entry.Message.TextContent() != "first" || next.Revisions[0].Entry.Message.Usage.InputTokens != 10 {
		t.Fatalf("revisions = %+v", next.Revisions)
	}
	next.Entries[0].Message.Content[1] = 'X'
	appendTestTail(t, path, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3}}}}`+"\n")
	later, err := tail.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if later.Revisions[0].Entry.Message.TextContent() != "second" {
		t.Fatal("caller corrupted fold state")
	}
}

func TestTailInvalidation(t *testing.T) {
	for _, kind := range []string{"truncate", "replace", "rewrite"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			original := `{"type":"user","message":{"role":"user","content":"original"}}` + "\n"
			writeTestFile(t, path, original)
			tail, _ := NewTail(path)
			if _, err := tail.Poll(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "truncate":
				writeTestFile(t, path, "")
			case "replace":
				other := path + ".new"
				writeTestFile(t, other, original)
				if err := os.Rename(other, path); err != nil {
					t.Fatal(err)
				}
			case "rewrite":
				writeTestFile(t, path, `{"type":"user","message":{"role":"user","content":"rewritten"}}`+"\n"+original)
			}
			for i := 0; i < 2; i++ {
				if _, err := tail.Poll(context.Background()); !errors.Is(err, ErrTailInvalid) {
					t.Fatalf("error = %v", err)
				}
			}
			tail.Reset()
			update, err := tail.Poll(context.Background())
			if err != nil || !update.Replace {
				t.Fatalf("reset = %+v, %v", update, err)
			}
		})
	}
}

func TestTailCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTestFile(t, path, "{\"type\":\"user\"}\n")
	tail, _ := NewTail(path)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tail.Poll(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	update, err := tail.Poll(context.Background())
	if err != nil || !update.Replace || len(update.Entries) != 1 {
		t.Fatalf("update = %+v, %v", update, err)
	}
}

func appendTestTail(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTailPartialUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	prefix := `{"type":"assistant","message":{"role":"assistant","content":"héllo"}}` + "\n"
	usage := `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":5}}}}`
	writeTestFile(t, path, prefix+usage)
	tail, _ := NewTail(path)
	update, err := tail.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if update.Partial == nil || update.Offset != int64(len(prefix)) || update.Entries[0].Message.Usage != nil {
		t.Fatalf("partial = %+v", update)
	}
	appendTestTail(t, path, "\n")
	update, err = tail.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Revisions) != 1 || update.Revisions[0].Entry.Message.Usage.InputTokens != 5 || update.Partial != nil {
		t.Fatalf("completed = %+v", update)
	}
}

func TestTailNativeSnapshots(t *testing.T) {
	for _, path := range []string{"testdata/pi/agent/sessions/proj/pi-compaction.jsonl", "testdata/opencode-compaction/storage/session/proj/ses_compact.json"} {
		t.Run(path, func(t *testing.T) {
			tail, _ := NewTail(path)
			want, err := ReadFile(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				update, err := tail.Poll(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if !update.Replace || !reflect.DeepEqual(update.Entries, want) {
					t.Fatal("native snapshot differs")
				}
			}
		})
	}
}

func TestTailLineLimitRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTestFile(t, path, "{\"type\":\"user\"}\n"+string(bytes.Repeat([]byte("x"), MaxLineSize)))
	tail, _ := NewTail(path)
	if _, err := tail.Poll(context.Background()); err == nil {
		t.Fatal("oversized line accepted")
	}
	if tail.offset != 0 || tail.count != 0 {
		t.Fatal("error advanced state")
	}
}

func BenchmarkTailAppend(b *testing.B) {
	path := filepath.Join(b.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		b.Fatal(err)
	}
	tail, _ := NewTail(path)
	line := []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := file.Write(line); err != nil {
			b.Fatal(err)
		}
		if _, err := tail.Poll(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func TestTailCodexFold(t *testing.T) {
	lines := []string{
		`{"type":"session_meta","payload":{"id":"session","cwd":"/work"}}`,
		`{"type":"turn_context","payload":{"model":"model-a"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10},"total_token_usage":{"input_tokens":10,"total_tokens":10}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":10},"total_token_usage":{"input_tokens":10,"total_tokens":10}},"rate_limits":{"primary":{"used_percent":20,"window_minutes":300,"resets_at":1000}}}}`,
		`{"type":"turn_context","payload":{"model":"model-b"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}]}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":3},"total_token_usage":{"input_tokens":13,"total_tokens":13}}}}`,
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeTestFile(t, path, "")
	tail, _ := NewTail(path)
	var accumulated []Entry
	var fullText string
	for i, line := range lines {
		fullText += line + "\n"
		appendTestTail(t, path, line+"\n")
		update, err := tail.Poll(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range update.Revisions {
			accumulated[r.Index] = r.Entry
		}
		accumulated = append(accumulated, update.Entries...)
		want, err := ReadAll(context.Background(), bytes.NewBufferString(fullText))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(accumulated, want) {
			t.Fatalf("line %d differs from full read", i)
		}
		if !reflect.DeepEqual(update.Summary, Summarize(tail.path, want)) {
			t.Fatalf("line %d summary differs", i)
		}
	}
}

// cancelTailContext makes cancellation during a poll deterministic.
type cancelTailContext struct {
	context.Context
	calls, after int
}

func (c *cancelTailContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}

func TestTailMidPollCancellation(t *testing.T) {
	for _, after := range []int{2, 3, 4} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTestFile(t, path, "{\"type\":\"user\"}\n{\"type\":\"assistant\"}\n")
			tail, _ := NewTail(path)
			ctx := &cancelTailContext{Context: context.Background(), after: after}
			if _, err := tail.Poll(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel = %v", err)
			}
			if tail.offset != 0 || tail.count != 0 {
				t.Fatal("cancellation committed state")
			}
			update, err := tail.Poll(context.Background())
			if err != nil || !update.Replace || len(update.Entries) != 2 {
				t.Fatalf("retry = %+v, %v", update, err)
			}
		})
	}
}

func BenchmarkTailCold(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tail, err := NewTail("testdata/sample-session.jsonl")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := tail.Poll(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}

func TestTailPreservesRawFields(t *testing.T) {
	for _, line := range []string{
		`{"type":"user","message":{"role":"user","content":[ { "type": "text", "text": "hello" } ]},"thinkingMetadata":{ "a": 1 },"todos":[ 1 ],"data":{ "b": 2 },"snapshot":{ "c": 3 }}`,
		`{"type":"attachment","attachment":{"type":"unknown","extra": "retained"}}`,
		`{"type":"user","toolUseResult":"User rejected tool use"}`,
	} {
		t.Run(line, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeTestFile(t, path, line+"\n")
			tail, _ := NewTail(path)
			update, err := tail.Poll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want, err := ReadFile(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(update.Entries, want) {
				t.Fatalf("tail changed raw fields: %+v", update.Entries)
			}
		})
	}
}
