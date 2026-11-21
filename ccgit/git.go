package ccgit

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitContext describes the git worktree at a given path.
// GitDir and GitCommonDir differ for linked worktrees: GitDir is
// .git/worktrees/<name>, GitCommonDir is the main repo's .git.
type GitContext struct {
	WorktreePath string `json:"worktree_path,omitempty"`
	GitDir       string `json:"git_dir,omitempty"`
	GitCommonDir string `json:"git_common_dir,omitempty"`
	Branch       string `json:"branch,omitempty"`
}

// ResolveGitContext returns the GitContext for path. An empty path
// resolves against the current working directory.
func ResolveGitContext(path string) (GitContext, error) {
	var ctx GitContext
	top, err := gitRevParse(path, "--show-toplevel")
	if err != nil {
		return ctx, err
	}
	ctx.WorktreePath = top
	if ctx.GitDir, err = gitRevParse(path, "--git-dir"); err != nil {
		return ctx, err
	}
	if ctx.GitCommonDir, err = gitRevParse(path, "--git-common-dir"); err != nil {
		return ctx, err
	}
	ctx.GitDir = absoluteGitPath(path, ctx.GitDir)
	ctx.GitCommonDir = absoluteGitPath(path, ctx.GitCommonDir)
	if branch, err := gitRevParse(path, "--abbrev-ref", "HEAD"); err == nil && branch != "HEAD" {
		ctx.Branch = branch
	}
	return ctx, nil
}

func gitRevParse(path string, args ...string) (string, error) {
	cmdArgs := []string{"-C", gitDirArg(path), "rev-parse"}
	cmdArgs = append(cmdArgs, args...)
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDirArg(path string) string {
	if path == "" {
		return "."
	}
	return path
}

// absoluteGitPath resolves a path git printed against the directory git ran
// in. git rev-parse reports --git-dir and --git-common-dir relative to its own
// working directory, not to the top of the worktree, so resolving them against
// the worktree misplaces every lookup made from a subdirectory.
func absoluteGitPath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	abs, err := filepath.Abs(filepath.Join(gitDirArg(dir), p))
	if err != nil {
		return filepath.Join(gitDirArg(dir), p)
	}
	return abs
}

// OriginRemote returns the origin remote of the repository at path,
// normalized to "host/path" with any scheme, credentials, and .git suffix
// removed, so that an ssh and an https clone of one repository compare equal.
// It returns an empty string when the repository has no origin.
func OriginRemote(path string) (string, error) {
	out, err := exec.Command("git", "-C", gitDirArg(path), "config", "--get", "remote.origin.url").Output()
	if err != nil {
		var exitErr *exec.ExitError
		// Git reports an unset key with status 1 and no diagnostic.
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && len(exitErr.Stderr) == 0 {
			return "", nil
		}
		return "", fmt.Errorf("git config remote.origin.url: %w", err)
	}
	return NormalizeRemote(strings.TrimSpace(string(out))), nil
}

// NormalizeRemote reduces a git remote URL to "host/path". It leaves anything
// it does not recognize, such as a bare local path, unchanged.
func NormalizeRemote(url string) string {
	u := strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if u == "" {
		return ""
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if i := strings.Index(u, "@"); i >= 0 && strings.Contains(u, ":") {
		// scp-style: git@github.com:tmc/cc
		u = strings.Replace(u[i+1:], ":", "/", 1)
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:] // strip credentials from an https URL
	}
	return strings.TrimSuffix(u, "/")
}
