// Package git internal tests for the per-root git-dir memo: a cached root must
// answer without re-running git, a foreign entry must be distrusted, and a memo
// the filesystem no longer agrees with must not be served.
package git

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitDir_cache verifies the memo contract from the inside. The roots here
// are NOT repositories, so a real resolution fails: an answer that comes back
// at all came from the memo, and an error means the memo was rejected.
func TestGitDir_cache(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: seed stores a synthetic entry for the root and returns the git
		//: directory the row expects back; "" means the memo must be refused.
		seed    func(t *testing.T, absRoot string) string
		wantErr bool
	}
	tests := []tc{
		{
			name: "a memo the filesystem still agrees with is served without git",
			seed: func(t *testing.T, absRoot string) string {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "memo-gitdir")
				mkdirT(t, dir)
				gitDirCache.Store(absRoot, gitDirMemo{
					dir:      dir,
					entry:    lstatOrNil(filepath.Join(absRoot, gitEntry)),
					resolved: lstatOrNil(dir),
				})
				//: The root is not a repository, so this can only come from the memo.
				return dir
			},
			wantErr: false,
		},
		{
			name: "a foreign-typed entry falls through to a real resolution",
			seed: func(t *testing.T, absRoot string) string {
				t.Helper()
				//: A non-memo entry must be distrusted, not type-panicked.
				gitDirCache.Store(absRoot, 42)
				return ""
			},
			wantErr: true,
		},
		{
			name: "a memo whose git directory vanished is not served",
			seed: func(t *testing.T, absRoot string) string {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "memo-gitdir")
				mkdirT(t, dir)
				gitDirCache.Store(absRoot, gitDirMemo{
					dir:      dir,
					entry:    lstatOrNil(filepath.Join(absRoot, gitEntry)),
					resolved: lstatOrNil(dir),
				})
				//: The repository moved away underneath the memo.
				if err := os.Remove(dir); err != nil {
					t.Fatalf("remove %s: %v", dir, err)
				}
				return ""
			},
			wantErr: true,
		},
		{
			name: "a memo is not served once the root grows its own .git",
			seed: func(t *testing.T, absRoot string) string {
				t.Helper()
				dir := filepath.Join(t.TempDir(), "memo-gitdir")
				mkdirT(t, dir)
				gitDirCache.Store(absRoot, gitDirMemo{
					dir:      dir,
					entry:    lstatOrNil(filepath.Join(absRoot, gitEntry)),
					resolved: lstatOrNil(dir),
				})
				//: A nearer repository now governs the root; the memoized one
				//: still exists, so only the root's own entry reveals it.
				mkdirT(t, filepath.Join(absRoot, gitEntry))
				return ""
			},
			wantErr: true,
		},
		{
			name:    "an unseeded non-repo root errors",
			seed:    func(t *testing.T, _ string) string { t.Helper(); return "" },
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := t.TempDir()
		abs, absErr := filepath.Abs(root)
		//: The cache key is the cleaned absolute root.
		if absErr != nil {
			t.Fatalf("abs(%s): %v", root, absErr)
		}
		abs = filepath.Clean(abs)
		want := c.seed(t, abs)

		got, err := GitDir(t.Context(), root)
		//: The error expectation must match the row.
		if (err != nil) != c.wantErr {
			t.Fatalf("GitDir(%s) error = %v, wantErr %v", root, err, c.wantErr)
		}
		//: The resolved value must match the seeded (or empty) expectation.
		if got != want {
			t.Errorf("GitDir(%s) = %q, want %q", root, got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
