package cc

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func BenchmarkReader(b *testing.B) {
	data, err := os.ReadFile("testdata/sample-session.jsonl")
	if err != nil {
		b.Skip("no sample")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for i := 0; i < b.N; i++ {
		r := NewReader(context.Background(), bytes.NewReader(data))
		for r.Next() {
		}
		if err := r.Err(); err != nil {
			b.Fatal(err)
		}
	}
}
