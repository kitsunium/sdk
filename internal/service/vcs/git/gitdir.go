// Package git — resolving the git directory that governs a path, once per root.
package git

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// gitEntry is the name of the `.git` entry a worktree carries: a directory in
// an ordinary repository, a `gitdir:` pointer FILE in a linked worktree or a
// submodule. Its identity is one half of the memo's validity condition.
const gitEntry string = ".git"

// gitDirCache memoizes GitDir results per cleaned absolute root so the caller
// pays the `git rev-parse --git-dir` subprocess once per root for as long as
// the answer stays true. Entries are gitDirMemo values; anything else found
// here is distrusted and recomputed.
var gitDirCache sync.Map

// gitDirMemo is one memoized resolution: the git directory, plus the two
// filesystem facts that make it still the answer.
//
// A memo with no validity condition is not a cache, it is an assertion that
// the filesystem does not change — and the two ways it changes under a
// long-lived process are both cheap to notice.
type gitDirMemo struct {
	// dir is the resolved absolute git directory.
	dir string
	// entry is the lstat of <root>/.git at resolution time, or nil when the
	// root carried no .git entry at all — which is the ordinary case for a
	// root that lies below the worktree top level.
	entry fs.FileInfo
	// resolved is the lstat of dir itself, so a git directory that moved or
	// was removed invalidates the memo even when <root>/.git is untouched.
	resolved fs.FileInfo
}

// GitDir resolves the git directory governing root — the directory that
// holds HEAD and the index — by shelling `git rev-parse --git-dir` and
// memoizing the result per root. It never assumes `.git` is a directory: in
// linked worktrees and submodules `.git` is a `gitdir:` pointer FILE, and
// rev-parse follows it to the real per-worktree git directory. The returned
// path is absolute and cleaned. An error means root is not inside a git
// repository (or git failed); failures are never memoized.
//
// The memo is re-validated on every call against two lstats — ~2.9 µs together
// where the subprocess they stand in for measures ~2.7 ms — and is served only
// while both still describe the resolution: the `.git` entry at root is the
// same file it was, or is still absent, and the resolved git directory is still
// the same directory. A repository that MOVES, one deleted and re-created, and
// a root that becomes its own repository under a parent one are each caught by
// that pair.
//
// What the pair does not catch is a repository created at a directory BETWEEN
// root and the worktree top level: root's own `.git` is still absent, the
// memoized git directory still exists, and only rev-parse's own discovery walk
// would notice that a nearer one now wins.
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
	//: Serve the memoized git dir while it still describes the filesystem.
	if dir, ok := memoizedGitDir(abs); ok {
		//: Memo hit — zero subprocesses.
		return dir, nil
	}
	entry := lstatOrNil(filepath.Join(abs, gitEntry))
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
	//: The root marker is read BEFORE the resolution and the git directory's
	//: after it, so a repository that changes underneath the subprocess fails
	//: its own next validation rather than being memoized as true.
	gitDirCache.Store(abs, gitDirMemo{dir: dir, entry: entry, resolved: lstatOrNil(dir)})
	//: Return the freshly resolved git directory.
	return dir, nil
}

// memoizedGitDir returns the memoized git directory for an already-cleaned
// absolute root, and whether it is still true.
func memoizedGitDir(abs string) (dir string, ok bool) {
	cached, found := gitDirCache.Load(abs)
	//: Nothing memoized for this root.
	if !found {
		//: Resolve it.
		return "", false
	}
	memo, isMemo := cached.(gitDirMemo)
	//: Two ways an entry is not an answer, and both cost one subprocess. Only
	//: gitDirMemo values are ever stored, so a foreign type is distrusted
	//: rather than asserted — the short-circuit is what keeps stillTrue off a
	//: zero memo. And a memo the filesystem no longer agrees with is stale.
	if !isMemo || !memo.stillTrue(abs) {
		//: Resolve it.
		return "", false
	}
	//: The memoized answer, still true.
	return memo.dir, true
}

// stillTrue reports whether this memo still describes the filesystem under abs.
func (m gitDirMemo) stillTrue(abs string) bool {
	//: The `.git` entry at the root decides which repository governs it: one
	//: that appeared makes a nearer repository win, and one whose identity
	//: changed is a different repository at the same path.
	if !sameEntry(m.entry, lstatOrNil(filepath.Join(abs, gitEntry))) {
		//: Stale.
		return false
	}
	//: The resolved directory itself must still be the one that was resolved.
	return sameEntry(m.resolved, lstatOrNil(m.dir))
}

// sameEntry reports whether two lstat results describe the same filesystem
// object, treating two absences as equal — an entry that was absent and still
// is has not changed, and that is the ordinary state of a root below the
// worktree top level.
func sameEntry(before, now fs.FileInfo) bool {
	//: Absence is a state, so it is compared rather than short-circuited.
	if before == nil || now == nil {
		//: Equal only when neither is there.
		return before == nil && now == nil
	}
	//: os.SameFile compares identity, not path, so an entry re-created under
	//: the same name is correctly a different object.
	return os.SameFile(before, now)
}

// lstatOrNil returns the lstat of path, or nil when it cannot be read. It does
// not follow a symbolic link, because a `.git` that became one is a change of
// the entry itself and not of whatever it now points at.
func lstatOrNil(path string) fs.FileInfo {
	info, err := os.Lstat(path)
	//: Unreadable and absent are the same fact here: there is no entry to
	//: compare, and the memo's validity turns on identity rather than on why.
	if err != nil {
		//: No entry.
		return nil
	}
	//: The entry as it is now.
	return info
}
