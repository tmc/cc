// Package ccgit resolves the git worktree context for a directory.
//
// [ResolveGitContext] runs git rev-parse to populate a [GitContext] with the
// worktree path, git dir, common git dir, and branch. GitDir and GitCommonDir
// differ for linked worktrees, which lets callers correlate a worktree back to
// its main repository.
//
//	ctx, err := ccgit.ResolveGitContext("")
//	if err != nil {
//		log.Fatal(err)
//	}
//	fmt.Println(ctx.Branch, ctx.GitCommonDir)
package ccgit
