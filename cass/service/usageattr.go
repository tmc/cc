package service

import (
	"context"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tmc/cc/cass"
	"github.com/tmc/cc/cass/store"
	"github.com/tmc/cc/ccgit"
)

// repoAttributor resolves the repository a session's usage belongs to and
// stamps it on every one of the session's usage events.
//
// Only one harness records a git context of its own, so most sessions are
// placed from a working directory: the directory is resolved with git when it
// still exists, and otherwise matched against the checkouts the index already
// knows about. Resolution is cached for the life of an index run, since
// thousands of sessions share a few hundred directories, and every repository
// the run resolves is written back so that a later run can place a session
// whose checkout has since been deleted.
//
// A session that cannot be placed keeps an empty git common dir. Its rows stay
// in every total and are reported as their own bucket; dropping them would make
// the repository view disagree with the provider view.
type repoAttributor struct {
	mu        sync.Mutex
	resolved  map[string]string // directory -> git common dir, "" when it did not resolve
	worktrees map[string]string // known checkout path -> git common dir, fixed at run start
	learned   map[string]string // checkouts this run resolved, applied a pass later
	exists    map[string]bool   // git common dir -> still on disk
	repos     map[string]store.Repo
	added     []store.Repo
}

// newRepoAttributor loads what previous index runs learned. A store error is
// not fatal: attribution degrades to what this run can resolve on its own.

func newRepoAttributor(ctx context.Context, s *store.DB) *repoAttributor {
	a := &repoAttributor{
		resolved:  make(map[string]string),
		worktrees: make(map[string]string),
		learned:   make(map[string]string),
		exists:    make(map[string]bool),
		repos:     make(map[string]store.Repo),
	}
	if paths, err := s.WorktreePaths(ctx); err == nil {
		a.worktrees = paths
	}
	if repos, err := s.Repos(ctx); err == nil {
		a.repos = repos
	}
	return a
}

// attribute stamps the repository onto every usage event of each session.
//
// It runs in two passes: every session that git or a stored context can place
// is placed first, and only then are the rest matched against the checkouts
// those passes revealed. A single pass would make a session's repository
// depend on which sessions happened to precede it, and collectors index
// concurrently, so that order is not stable from run to run.
func (a *repoAttributor) attribute(sessions []cass.Session) {
	unplaced := make([]int, 0, len(sessions))
	for i := range sessions {
		sess := &sessions[i]
		if len(sess.Usage) == 0 {
			continue
		}
		dir, how := a.resolve(sess)
		if dir == "" {
			unplaced = append(unplaced, i)
			continue
		}
		if how == "path" {
			// Recorded on the session so that a later run reaches the same
			// answer from the index alone, even once the checkout is gone.
			// A prefix match is a guess and is not persisted.
			sess.GitCommonDir = dir
		}
		a.stamp(sess, dir, how)
	}
	for _, i := range unplaced {
		sess := &sessions[i]
		dir := ""
		if cwd := sessionCWD(sess); cwd != "" {
			a.mu.Lock()
			dir = a.byPrefix(cwd)
			a.mu.Unlock()
		}
		if dir == "" {
			a.stamp(sess, "", "")
			continue
		}
		a.stamp(sess, dir, "prefix")
	}
}

// stamp writes one attribution onto all of a session's usage events.
func (a *repoAttributor) stamp(sess *cass.Session, dir, how string) {
	for j := range sess.Usage {
		sess.Usage[j].GitCommonDir, sess.Usage[j].Attribution = dir, how
	}
}

// resolve returns the git common dir for a session and the step that found it.
func (a *repoAttributor) resolve(sess *cass.Session) (dir, how string) {
	if sess.GitCommonDir != "" {
		a.mu.Lock()
		a.note(sess.GitCommonDir, sess.Workspace)
		a.mu.Unlock()
		return sess.GitCommonDir, "git"
	}
	cwd := sessionCWD(sess)
	if cwd == "" {
		return "", ""
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if dir, ok := a.resolved[cwd]; ok {
		if dir != "" {
			return dir, "path"
		}
	} else {
		dir := ""
		if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
			if gc, err := ccgit.ResolveGitContext(cwd); err == nil {
				dir = gc.GitCommonDir
				a.note(dir, gc.WorktreePath)
			}
		}
		a.resolved[cwd] = dir
		if dir != "" {
			return dir, "path"
		}
	}
	return "", ""
}

// note records a resolved repository and the checkout it was reached through.
// The caller holds a.mu.
func (a *repoAttributor) note(dir, worktree string) {
	if dir == "" {
		return
	}
	if worktree != "" {
		if _, ok := a.learned[worktree]; !ok {
			a.learned[worktree] = dir
		}
	}
	if _, ok := a.repos[dir]; ok {
		return
	}
	repo := store.Repo{GitCommonDir: dir, Name: repoName(dir, "")}
	if remote, err := ccgit.OriginRemote(repoRoot(dir)); err == nil && remote != "" {
		repo.Remote, repo.Name = remote, remote
		repo.ResolvedAt = time.Now().Unix()
	} else if _, err := os.Stat(dir); err == nil {
		repo.ResolvedAt = time.Now().Unix()
	}
	a.repos[dir] = repo
	a.added = append(a.added, repo)
}

// byPrefix returns the git common dir of the longest known checkout that
// contains cwd. This places a working directory that was a subdirectory of a
// checkout, and one whose checkout has since been deleted. The caller holds
// a.mu.
//
// A checkout whose git common dir still exists always outranks one that does
// not, however much less of the path it matches: an earlier ccgit defect wrote
// directories that were never real, and a phantom that wins here scatters one
// repository across two keys. A genuinely deleted checkout still places its
// own sessions, which is what makes a removed repository keep its history.
func (a *repoAttributor) byPrefix(cwd string) string {
	best, bestLen, bestReal := "", 0, false
	consider := func(path, dir string) {
		if !under(cwd, path) {
			return
		}
		real, ok := a.exists[dir]
		if !ok {
			_, err := os.Stat(dir)
			real = err == nil
			a.exists[dir] = real
		}
		if bestReal && !real {
			return
		}
		if real == bestReal && len(path) <= bestLen {
			return
		}
		best, bestLen, bestReal = dir, len(path), real
	}
	for path, dir := range a.worktrees {
		consider(path, dir)
	}
	for path, dir := range a.learned {
		consider(path, dir)
	}
	return best
}

// flush persists the repositories this run resolved.
func (a *repoAttributor) flush(ctx context.Context, s *store.DB) error {
	a.mu.Lock()
	added := a.added
	a.added = nil
	a.mu.Unlock()
	return s.SaveRepos(ctx, added)
}

// under reports whether path is dir or sits inside it.
func under(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// sessionCWD returns the directory a session's requests ran in: the one most of
// its requests recorded, falling back to the session workspace. Codex and
// opencode record a directory per request, and a session that changed
// directories belongs to where it did most of its work.
func sessionCWD(sess *cass.Session) string {
	counts := make(map[string]int)
	for _, u := range sess.Usage {
		if u.CWD != "" {
			counts[u.CWD]++
		}
	}
	if len(counts) == 0 {
		return sess.Workspace
	}
	dirs := make([]string, 0, len(counts))
	for dir := range counts {
		dirs = append(dirs, dir)
	}
	// Sorted so that a tie between two equally used directories resolves the
	// same way on every run, which keeps re-indexing idempotent.
	sort.Strings(dirs)
	best := dirs[0]
	for _, dir := range dirs[1:] {
		if counts[dir] > counts[best] {
			best = dir
		}
	}
	return best
}

// repoRoot returns the checkout a git common dir belongs to.
func repoRoot(gitCommonDir string) string {
	if filepath.Base(gitCommonDir) == ".git" {
		return filepath.Dir(gitCommonDir)
	}
	return gitCommonDir
}

// repoName returns the display name for a repository: its remote when one is
// known, else the checkout path relative to $GOPATH/src or $HOME, else the
// basename.
func repoName(gitCommonDir, remote string) string {
	if remote != "" {
		return remote
	}
	root := repoRoot(gitCommonDir)
	var bases []string
	// GOPATH has not had to be exported since modules, so it comes from the
	// go tool's own default rather than the environment.
	for _, gopath := range filepath.SplitList(build.Default.GOPATH) {
		if gopath != "" {
			bases = append(bases, filepath.Join(gopath, "src"))
		}
	}
	bases = append(bases, os.Getenv("HOME"))
	for _, base := range bases {
		if base == "" {
			continue
		}
		if rel, err := filepath.Rel(base, root); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	// The configured GOPATH need not be the tree the sources live in, so a
	// checkout under any src directory is named relative to it.
	if i := strings.LastIndex(root, "/src/"); i >= 0 {
		return root[i+len("/src/"):]
	}
	return filepath.Base(root)
}
