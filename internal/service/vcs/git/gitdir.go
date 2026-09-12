// Package git — resolving the git directory that governs a path, once per root.
package git

import (
	"context"
	"path/filepath"
	"sync"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// gitDirCache memoizes ResolveGitDir results per cleaned absolute root so the
// daemon pays the `git rev-parse --git-dir` subprocess once per root, ever.
var gitDirCache sync.Map

// GitDir resolves the git directory governing root — the directory that
// holds HEAD and the index — by shelling `git rev-parse --git-dir` once and
// caching the result per root. It never assumes `.git` is a directory: in
// linked worktrees and submodules `.git` is a `gitdir:` pointer FILE, and
// rev-parse follows it to the real per-worktree git directory. The returned
// path is absolute and cleaned. An error means root is not inside a git
// repository (or git failed); failures are NOT cached so a later `git init`
// is picked up on the next call.
func GitDir(ctx context.Context, root string) (gitDir string, err error) {
	abs, err := filepath.Abs(root)
	//: An unresolvable root cannot anchor a cache key or a git invocation.
	if err != nil {
		//: Typed refusal: the caller cannot act on an os-level path error here.
		return "", errs.Wrap(err, errs.WrapParams{
			Code:    corevcs.CodeRepositoryUnresolved,
			Reason:  "REPOSITORY_UNRESOLVED",
			Public:  "the path is not inside a readable repository",
			Private: "service/vcs/git: the root path could not be made absolute: " + root,
		})
	}
	abs = filepath.Clean(abs)
	//: Serve the memoized git dir when this root was already resolved.
	if cached, ok := gitDirCache.Load(abs); ok {
		//: Only strings are ever stored; a foreign type falls through to a recompute.
		if dir, isString := cached.(string); isString {
			//: Memo hit — zero subprocesses.
			return dir, nil
		}
	}
	out, err := runGitOutput(ctx, abs, "rev-parse", "--git-dir")
	//: A failed probe means "no repository here" — never cache failures.
	if err != nil {
		//: Re-label as REPOSITORY_UNRESOLVED rather than passing the generic
		//: COMMAND_FAILED up. This IS the condition that sentinel names, and a
		//: caller cannot tell "not a repository" from "git broke" otherwise.
		//: The cause travels, so the git stderr is still reachable.
		return "", errs.Wrap(err, errs.WrapParams{
			Code:    corevcs.CodeRepositoryUnresolved,
			Reason:  "REPOSITORY_UNRESOLVED",
			Public:  "the path is not inside a readable repository",
			Private: "service/vcs/git: rev-parse --git-dir failed for " + abs,
		})
	}
	dir := out
	//: rev-parse may answer with a root-relative path (".git"); anchor it so
	//: callers can watch it regardless of their working directory.
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(abs, dir)
	}
	dir = filepath.Clean(dir)
	gitDirCache.Store(abs, dir)
	//: Return the freshly resolved git directory.
	return dir, nil
}
