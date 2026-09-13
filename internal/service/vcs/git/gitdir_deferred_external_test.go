// Package git_test drives the git-dir memo against repositories that change
// under it — the two shapes ADR 0076 §Deferred left open.
package git_test

import (
	"os"
	"path/filepath"
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// TestGitDirNoticesARootThatBecomesItsOwnRepository is the correction to the
// claim ADR 0076 made about this memo: that a root becoming a repository later
// is picked up because failures are not cached.
//
// It holds only when the first call FAILED. A root that already lay inside a
// parent repository resolved successfully, so the parent's git directory was
// memoized — and `git init` inside that root then made a nearer repository the
// governing one while the memo kept answering with the parent's. Observed
// before the fix: GitDir returned <outer>/.git where `git rev-parse
// --absolute-git-dir` in the same directory returned <outer>/sub/.git.
func TestGitDirNoticesARootThatBecomesItsOwnRepository(t *testing.T) {
	t.Parallel()
	outer := initRepo(t)
	writeRepoFile(t, outer, "sub/a.go", "package sub\n")
	runGit(t, outer, "add", ".")
	runGit(t, outer, "commit", "-m", "sub")
	sub := filepath.Join(outer, "sub")

	first, err := gitpkg.GitDir(t.Context(), sub)
	//: The first resolution succeeds, which is what makes the memo stick.
	if err != nil {
		t.Fatalf("GitDir(%s) = %v", sub, err)
	}
	if first != filepath.Join(outer, ".git") {
		t.Fatalf("GitDir(%s) = %q, want the outer repository's git dir", sub, first)
	}

	runGit(t, sub, "init", "-b", "main")
	want := runGit(t, sub, "rev-parse", "--absolute-git-dir")

	second, err := gitpkg.GitDir(t.Context(), sub)
	//: A nearer repository now governs the root.
	if err != nil {
		t.Fatalf("GitDir(%s) after init = %v", sub, err)
	}
	if second != filepath.Clean(want) {
		t.Errorf("GitDir(%s) = %q, want %q — git's own answer for the same directory", sub, second, want)
	}
}

// TestGitDirNoticesARepositoryThatMoved pins the other shape. A memo that is
// never invalidated hands back a path that no longer exists, and a caller that
// watches HEAD there watches nothing for the rest of the process's life.
// Observed before the fix: GitDir returned <moved>/.git after the directory had
// been renamed away, and os.Stat on that answer failed with ENOENT.
func TestGitDirNoticesARepositoryThatMoved(t *testing.T) {
	t.Parallel()
	root := initRepo(t)
	first, err := gitpkg.GitDir(t.Context(), root)
	//: The repository is real and the answer is memoized.
	if err != nil {
		t.Fatalf("GitDir(%s) = %v", root, err)
	}
	if _, statErr := os.Stat(first); statErr != nil {
		t.Fatalf("the first answer %q does not exist: %v", first, statErr)
	}

	moved := filepath.Join(filepath.Dir(root), "moved-"+filepath.Base(root))
	//: A rename within one filesystem is the cheapest way a repository moves.
	if err := os.Rename(root, moved); err != nil {
		t.Skipf("cannot move the repository here: %v", err)
	}

	second, err := gitpkg.GitDir(t.Context(), root)
	//: The root is no longer a repository — nor a directory — so the only
	//: honest answer is a refusal, never a path nothing is at.
	if err == nil {
		t.Fatalf("GitDir(%s) = %q after the repository moved, want a refusal", root, second)
	}
	//: And the memo must not have been replaced by a second stale entry.
	if second != "" {
		t.Errorf("GitDir returned %q alongside an error", second)
	}
}
