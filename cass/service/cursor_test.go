package service

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tmc/cc/cass"
)

func TestScanSince(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	recent := now.Add(-time.Hour)
	old := now.Add(-90 * 24 * time.Hour)

	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{"empty", "", time.Time{}},
		{"unparseable", "not-a-number", time.Time{}},
		{"recent cursor is incremental", strconv.FormatInt(recent.Unix(), 10), recent},
		{"old cursor is still incremental", strconv.FormatInt(old.Unix(), 10), old},
		{"future cursor forces full walk", strconv.FormatInt(now.Add(time.Hour).Unix(), 10), time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanSince(tt.in, now); !got.Equal(tt.want) {
				t.Errorf("scanSince(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// sinceCollector emits one session whose mtime is fileTime, honoring the
// modified-since floor the way a real collector's directory walk does.
type sinceCollector struct {
	fileTime time.Time
	got      time.Time
}

func (c *sinceCollector) Name() string { return "claude-code" }

func (c *sinceCollector) Detect(context.Context) (*cass.DetectionResult, error) {
	return &cass.DetectionResult{Agent: c.Name(), Found: true, Paths: []string{"p"}}, nil
}

func (c *sinceCollector) Scan(ctx context.Context, cfg cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	c.got = cfg.Since
	if !cfg.Since.IsZero() && c.fileTime.Before(cfg.Since) {
		return nil
	}
	out <- cass.Session{ID: "s1", Agent: c.Name(), StartedAt: c.fileTime}
	return nil
}

func TestIndexPassesCursorFloor(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	cursor := now.Add(-time.Hour).Truncate(time.Second)

	col := &sinceCollector{fileTime: now.Add(-90 * 24 * time.Hour)}
	svc := newTestService(t, col)

	ts := strconv.FormatInt(cursor.Unix(), 10)
	if err := svc.store.SetMeta(ctx, "last_indexed_at", ts); err != nil {
		t.Fatal(err)
	}

	n, err := svc.Index(ctx, false)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if n != 0 {
		t.Errorf("Index = %d, want 0", n)
	}
	if !col.got.Equal(cursor) {
		t.Errorf("scan since = %v, want %v", col.got, cursor)
	}
}

// flakyCollector fails detection or scanning on demand, and otherwise emits one
// session.
type flakyCollector struct {
	detectErr error
	scanErr   error
}

func (c *flakyCollector) Name() string { return "claude-code" }

func (c *flakyCollector) Detect(context.Context) (*cass.DetectionResult, error) {
	if c.detectErr != nil {
		return nil, c.detectErr
	}
	return &cass.DetectionResult{Agent: c.Name(), Found: true, Paths: []string{"p"}}, nil
}

func (c *flakyCollector) Scan(ctx context.Context, cfg cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	if c.scanErr != nil {
		return c.scanErr
	}
	out <- cass.Session{ID: "s1", Agent: c.Name(), StartedAt: time.Now()}
	return nil
}

func TestIndexCursorAdvancesOnlyOnCleanWalk(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		col     *flakyCollector
		wantErr bool
	}{
		{"clean walk advances", &flakyCollector{}, false},
		{"detect failure holds cursor", &flakyCollector{detectErr: errors.New("boom")}, true},
		{"scan failure holds cursor", &flakyCollector{scanErr: errors.New("boom")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t, tt.col)

			before := strconv.FormatInt(time.Now().Add(-90*24*time.Hour).Unix(), 10)
			if err := svc.store.SetMeta(ctx, "last_indexed_at", before); err != nil {
				t.Fatal(err)
			}

			_, err := svc.Index(ctx, false)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Index error = %v, want error %v", err, tt.wantErr)
			}

			after, err := svc.store.Meta(ctx, "last_indexed_at")
			if err != nil {
				t.Fatal(err)
			}
			if moved := after != before; moved == tt.wantErr {
				t.Errorf("cursor moved = %v (%q -> %q), want %v", moved, before, after, !tt.wantErr)
			}
		})
	}
}

// slowCollector scans slowly enough that a cursor stamped when the walk ended
// is distinguishable from one stamped when it began.
type slowCollector struct {
	delay time.Duration
}

func (c *slowCollector) Name() string { return "claude-code" }

func (c *slowCollector) Detect(context.Context) (*cass.DetectionResult, error) {
	return &cass.DetectionResult{Agent: c.Name(), Found: true, Paths: []string{"p"}}, nil
}

func (c *slowCollector) Scan(ctx context.Context, cfg cass.ScanConfig, out chan<- cass.Session) error {
	defer close(out)
	time.Sleep(c.delay)
	out <- cass.Session{ID: "s1", Agent: c.Name(), StartedAt: time.Now()}
	return nil
}

func TestIndexCursorRecordsWalkStart(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t, &slowCollector{delay: 2 * time.Second})

	start := time.Now()
	if _, err := svc.Index(ctx, false); err != nil {
		t.Fatalf("Index: %v", err)
	}

	ts, err := svc.store.Meta(ctx, "last_indexed_at")
	if err != nil {
		t.Fatal(err)
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		t.Fatalf("last_indexed_at %q: %v", ts, err)
	}
	if got := time.Unix(unix, 0); got.After(start.Add(time.Second)) {
		t.Errorf("cursor = %v, want no later than walk start %v", got, start)
	}
}

func newTestService(t *testing.T, col cass.Collector) *Service {
	t.Helper()
	svc, err := New(Config{
		DBPath:     filepath.Join(t.TempDir(), "index.db"),
		Collectors: []cass.Collector{col},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}
