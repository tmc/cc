package cc

import (
	"path/filepath"
	"testing"
)

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
