package ccgit

import (
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
	ctx.GitDir = absoluteGitPath(top, ctx.GitDir)
	ctx.GitCommonDir = absoluteGitPath(top, ctx.GitCommonDir)
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

func absoluteGitPath(worktree, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(worktree, p)
}
