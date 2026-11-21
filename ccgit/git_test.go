package ccgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run executes a git command in dir, skipping the test if git is unusable.
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Skipf("git %v: %v: %s", args, err, out)
	}
}

func TestResolveGitContext(t *testing.T) {
	ctx, err := ResolveGitContext(".")
	if err != nil {
		t.Fatalf("ResolveGitContext: %v", err)
	}
	if !filepath.IsAbs(ctx.WorktreePath) {
		t.Errorf("WorktreePath not absolute: %q", ctx.WorktreePath)
	}
	if !filepath.IsAbs(ctx.GitDir) {
		t.Errorf("GitDir not absolute: %q", ctx.GitDir)
	}
	if !filepath.IsAbs(ctx.GitCommonDir) {
		t.Errorf("GitCommonDir not absolute: %q", ctx.GitCommonDir)
	}
}

// TestResolveGitContextFromSubdirectory checks that a lookup made from inside a
// repository reports the repository's own git directory. git prints --git-dir
// relative to where git ran, so resolving it against the worktree root turned
// every subdirectory lookup into a path outside the repository.
func TestResolveGitContextFromSubdirectory(t *testing.T) {
	repo := t.TempDir()
	run(t, repo, "init", "-q")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	top, err := ResolveGitContext(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveGitContext(sub)
	if err != nil {
		t.Fatal(err)
	}
	// Compared through EvalSymlinks: git prints an absolute path from a
	// subdirectory and a relative one from the top, and on macOS the temp
	// directory is reached through a symlink, so the two spellings differ.
	if real(t, got.GitCommonDir) != real(t, top.GitCommonDir) {
		t.Errorf("git common dir from %s = %q, want %q", sub, got.GitCommonDir, top.GitCommonDir)
	}
	if real(t, got.GitDir) != real(t, top.GitDir) {
		t.Errorf("git dir from %s = %q, want %q", sub, got.GitDir, top.GitDir)
	}
	if _, err := os.Stat(got.GitCommonDir); err != nil {
		t.Errorf("resolved git common dir does not exist: %v", err)
	}
}

// real resolves a path through any symlinks in it.
func real(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOriginRemote(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	run(t, repo, "init", "-q")

	if got, err := OriginRemote(repo); got != "" || err != nil {
		t.Errorf("OriginRemote without origin = %q, %v; want empty string, nil", got, err)
	}
	run(t, repo, "remote", "add", "origin", "git@example.com:org/repo.git")
	if got, err := OriginRemote(repo); got != "example.com/org/repo" || err != nil {
		t.Errorf("OriginRemote with origin = %q, %v; want %q, nil", got, err, "example.com/org/repo")
	}
	if _, err := OriginRemote(filepath.Join(repo, "missing")); err == nil {
		t.Error("OriginRemote for missing directory succeeded")
	}
}

// NormalizeRemote reduces the URL forms one repository is cloned by to one key.
func TestNormalizeRemote(t *testing.T) {
	tests := []struct{ url, want string }{
		{"git@github.com:tmc/cc.git", "github.com/tmc/cc"},
		{"https://github.com/tmc/cc.git", "github.com/tmc/cc"},
		{"https://tmc@github.com/tmc/cc", "github.com/tmc/cc"},
		{"ssh://git@github.com/tmc/cc.git", "github.com/tmc/cc"},
		{"/srv/git/local", "/srv/git/local"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeRemote(tt.url); got != tt.want {
			t.Errorf("NormalizeRemote(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
