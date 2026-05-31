package collector

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/cc/cass"
)

func TestUnsupportedCollectorsReturnScanError(t *testing.T) {
	tests := []struct {
		name string
		col  cass.Collector
		want string
	}{
		{"cursor", &Cursor{}, "cursor scan not implemented"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := make(chan cass.Session)
			err := tt.col.Scan(context.Background(), cass.ScanConfig{}, out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Scan error = %v, want %q", err, tt.want)
			}
			if _, ok := <-out; ok {
				t.Fatalf("Scan left output channel open")
			}
		})
	}
}
