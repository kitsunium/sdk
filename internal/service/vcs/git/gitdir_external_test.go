// Package git_test exercises changed-set resolution against real temp repos.
package git_test

import (
	"os"
	"path/filepath"
	"testing"

	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// TestGitDir drives GitDir against real repositories: a normal
// repo (`.git` directory), a linked worktree (`.git` is a `gitdir:` pointer
// file that must be followed to the per-worktree git dir), and a plain
// directory with no repository at all.
func TestGitDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
		//: wantGitFile asserts that <root>/.git is a regular FILE (the
		//: worktree `gitdir:` pointer case) before resolution runs.
		wantGitFile bool
	}{
		{
			name: "normal repo resolves .git directory",
			setup: func(t *testing.T) string {
				t.Helper()
				//: A standard repo: .git is a directory at the top level.
				return initRepo(t)
			},
			wantErr:     false,
			wantGitFile: false,
		},
		{
			name: "linked worktree follows the gitdir pointer file",
			setup: func(t *testing.T) string {
				t.Helper()
				main := initRepo(t)
				wt := filepath.Join(t.TempDir(), "wt")
				//: `git worktree add` writes a `gitdir:` pointer FILE at wt/.git.
				runGit(t, main, "worktree", "add", "-b", "feature-wt", wt)
				//: Canonicalise like initRepo so paths compare cleanly.
				return runGit(t, wt, "rev-parse", "--show-toplevel")
			},
			wantErr:     false,
			wantGitFile: true,
		},
		{
			name: "outside any repository errors",
			setup: func(t *testing.T) string {
				t.Helper()
				//: A bare temp dir with no .git anywhere above it... t.TempDir
				//: parents are tmpfs roots, never repositories.
				return t.TempDir()
			},
			wantErr:     true,
			wantGitFile: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := tt.setup(t)

			//: Pre-assert the fixture shape: the worktree case MUST present
			//: `.git` as a pointer file, proving naive `.git/index` watching
			//: would have failed here.
			if tt.wantGitFile {
				fi, statErr := os.Stat(filepath.Join(root, ".git"))
				//: The fixture is broken if .git is missing or a directory.
				if statErr != nil || fi.IsDir() {
					t.Fatalf(".git at %s: err=%v isDir=%v, want regular gitdir pointer file", root, statErr, fi != nil && fi.IsDir())
				}
			}

			dir, err := gitpkg.GitDir(t.Context(), root)
			//: Error expectation must match the fixture.
			if (err != nil) != tt.wantErr {
				t.Fatalf("GitDir(%s) error = %v, wantErr %v", root, err, tt.wantErr)
			}
			//: Error rows have nothing further to verify.
			if tt.wantErr {
				//: Done — the failure path was the assertion.
				return
			}

			//: The resolved git dir must be absolute, a real directory, and
			//: contain HEAD — the file the daemon watches for invalidation.
			if !filepath.IsAbs(dir) {
				t.Fatalf("GitDir(%s) = %q, want absolute path", root, dir)
			}
			fi, statErr := os.Stat(dir)
			//: A git dir that is not a directory cannot be watched.
			if statErr != nil || !fi.IsDir() {
				t.Fatalf("resolved git dir %q: err=%v isDir=%v, want existing directory", dir, statErr, fi != nil && fi.IsDir())
			}
			//: HEAD must live directly inside the resolved dir.
			if _, headErr := os.Stat(filepath.Join(dir, "HEAD")); headErr != nil {
				t.Fatalf("resolved git dir %q has no HEAD: %v", dir, headErr)
			}

			again, againErr := gitpkg.GitDir(t.Context(), root)
			//: The memoized second call must agree with the first.
			if againErr != nil || again != dir {
				t.Fatalf("second GitDir(%s) = (%q, %v), want (%q, nil)", root, again, againErr, dir)
			}
		})
	}
}
