package service

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccusage"
)

// initRepo makes a real git repository, since attribution shells out to git
// and a fake directory would only test the fallbacks.
func initRepo(t *testing.T, dir, remote string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", remote},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v: %s", args, err, out)
		}
	}
	// macOS resolves the temp dir through a symlink; git reports the real path.
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(real, ".git")
}

func usageSession(id, workspace, gitCommonDir string, cwds ...string) cass.Session {
	sess := cass.Session{ID: id, Agent: "codex-cli", Workspace: workspace, GitCommonDir: gitCommonDir}
	if len(cwds) == 0 {
		cwds = []string{""}
	}
	for _, cwd := range cwds {
		sess.Usage = append(sess.Usage, cass.UsageEvent{Record: ccusage.Record{CWD: cwd, Input: 1}})
	}
	return sess
}

// TestAttribute covers each step of the attribution order against a real
// repository, a subdirectory of it, a path that no longer exists, and a
// session with nothing to go on.
func TestAttribute(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	gitDir := initRepo(t, repo, "git@github.com:tmc/cc.git")
	repoPath := filepath.Dir(gitDir)
	sub := filepath.Join(repoPath, "cass", "store")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	s := newTestStoreDB(t)
	ctx := context.Background()
	attr := newRepoAttributor(ctx, s)

	sessions := []cass.Session{
		usageSession("common-dir", "", "/from/collector/.git"),
		usageSession("workspace", repoPath, ""),
		usageSession("subdir", sub, ""),
		usageSession("per-turn-cwd", "", "", sub, sub, filepath.Join(root, "elsewhere")),
		usageSession("gone", filepath.Join(repoPath, "deleted", "deeper"), ""),
		usageSession("nothing", "", ""),
	}
	attr.attribute(sessions)

	want := []struct{ dir, how string }{
		{"/from/collector/.git", "git"},
		{gitDir, "path"},
		{gitDir, "path"},
		{gitDir, "path"},
		{gitDir, "prefix"},
		{"", ""},
	}
	for i, sess := range sessions {
		got := sess.Usage[0]
		if got.GitCommonDir != want[i].dir || got.Attribution != want[i].how {
			t.Errorf("%s: attributed to %q by %q, want %q by %q",
				sess.ID, got.GitCommonDir, got.Attribution, want[i].dir, want[i].how)
		}
		for _, u := range sess.Usage {
			if u.GitCommonDir != got.GitCommonDir {
				t.Errorf("%s: events disagree: %q and %q", sess.ID, got.GitCommonDir, u.GitCommonDir)
			}
		}
	}

	if err := attr.flush(ctx, s); err != nil {
		t.Fatal(err)
	}
	repos, err := s.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := repos[gitDir]; got.Remote != "github.com/tmc/cc" || got.Name != "github.com/tmc/cc" {
		t.Errorf("repo %s = %+v, want the normalized origin", gitDir, got)
	}
	if repos[gitDir].ResolvedAt == 0 {
		t.Error("resolved repo has no resolved_at")
	}
}

// TestAttributePrefersMostFrequentCWD checks that a session that moved between
// directories is attributed to where it did most of its work, and that a tie
// resolves the same way every run so re-indexing stays idempotent.
func TestAttributePrefersMostFrequentCWD(t *testing.T) {
	sess := usageSession("s", "/fallback", "", "/b", "/a", "/a")
	if got := sessionCWD(&sess); got != "/a" {
		t.Errorf("cwd = %q, want /a", got)
	}
	tie := usageSession("s", "/fallback", "", "/b", "/a")
	if got := sessionCWD(&tie); got != "/a" {
		t.Errorf("tie cwd = %q, want the lowest of the tied directories", got)
	}
	none := usageSession("s", "/fallback", "")
	if got := sessionCWD(&none); got != "/fallback" {
		t.Errorf("cwd with no per-request directory = %q, want the workspace", got)
	}
}

// TestAttributeUsesPriorRuns checks that a checkout learned by an earlier index
// run places a session whose directory no longer exists. This is what makes a
// moved or deleted repository keep its history.
func TestAttributeUsesPriorRuns(t *testing.T) {
	s := newTestStoreDB(t)
	ctx := context.Background()
	err := s.BatchIndex(ctx, []cass.Session{
		{ID: "old", Workspace: "/gone/repo", GitCommonDir: "/gone/repo/.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	attr := newRepoAttributor(ctx, s)
	sessions := []cass.Session{usageSession("new", "/gone/repo/pkg", "")}
	attr.attribute(sessions)
	got := sessions[0].Usage[0]
	if got.GitCommonDir != "/gone/repo/.git" || got.Attribution != "prefix" {
		t.Errorf("attributed to %q by %q, want /gone/repo/.git by prefix", got.GitCommonDir, got.Attribution)
	}
}

// TestRepoName checks the display name fallbacks for a repository with no
// remote.
func TestRepoName(t *testing.T) {
	t.Setenv("GOPATH", "/go")
	t.Setenv("HOME", "/home/example")
	tests := []struct {
		dir, remote, want string
	}{
		{"/go/src/github.com/tmc/cc/.git", "", "github.com/tmc/cc"},
		{"/go/src/github.com/tmc/cc/.git", "github.com/tmc/cc", "github.com/tmc/cc"},
		{"/home/example/tmp/scratch/.git", "", "tmp/scratch"},
		{"/elsewhere/thing/.git", "", "thing"},
		{"/elsewhere/bare.git", "", "bare.git"},
	}
	for _, tt := range tests {
		if got := repoName(tt.dir, tt.remote); got != tt.want {
			t.Errorf("repoName(%q, %q) = %q, want %q", tt.dir, tt.remote, got, tt.want)
		}
	}
}

// newTestStoreDB opens an empty in-memory index.
func newTestStoreDB(t *testing.T) *store.DB {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
