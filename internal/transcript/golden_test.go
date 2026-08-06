package transcript_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/cc"
	"github.com/tmc/cc/internal/transcript"
)

var update = flag.Bool("update", false, "update transcript golden files")

func TestGolden(t *testing.T) {
	for _, name := range []string{"claude", "codex", "antigravity", "pi"} {
		t.Run(name, func(t *testing.T) {
			in := filepath.Join("testdata", name+".json")
			data, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			var entries []cc.Entry
			if err := json.Unmarshal(data, &entries); err != nil {
				t.Fatalf("decode %s: %v", in, err)
			}
			got := transcript.Render(entries, transcript.Options{
				Width: 60,
				Style: transcript.StyleFor(name),
			})
			golden := filepath.Join("testdata", name+".txt")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("render differs from %s (-want +got):\n%s", golden, lineDiff(string(want), got))
			}
		})
	}
}

func lineDiff(want, got string) string {
	var b strings.Builder
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	n := len(w)
	if len(g) > n {
		n = len(g)
	}
	for i := 0; i < n; i++ {
		var ws, gs string
		if i < len(w) {
			ws = w[i]
		}
		if i < len(g) {
			gs = g[i]
		}
		if ws != gs {
			b.WriteString("- " + ws + "\n+ " + gs + "\n")
		}
	}
	return b.String()
}
