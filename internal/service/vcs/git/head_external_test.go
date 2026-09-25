// External tests for head.go - git package.
package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corevcs "github.com/kitsunium/sdk/internal/core/vcs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	gitpkg "github.com/kitsunium/sdk/internal/service/vcs/git"
)

// committedAt is the committer date the head fixture records, in an offset
// that is not UTC so a lost offset shows.
const committedAt string = "2026-09-24T10:00:00+02:00"

// headRepo initialises a repository whose single commit carries committedAt as
// its committer date, with one tracked file, and returns its root.
func headRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE="+committedAt, "GIT_AUTHOR_DATE="+committedAt)
		//: a broken fixture must fail loudly rather than test nothing.
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	writeRepoFile(t, root, "tracked.go", "package p\n")
	writeRepoFile(t, root, "sub/nested.go", "package sub\n")
	git("add", ".")
	git("commit", "-q", "-m", "base")
	return root
}

// TestHead pins the three facts and the one line drawn through "modified":
// a tracked change — staged or not — counts, an untracked file does not.
func TestHead(t *testing.T) {
	t.Parallel()
	want, err := time.Parse(time.RFC3339, committedAt)
	if err != nil {
		t.Fatalf("parse fixture date: %v", err)
	}
	type tc struct {
		name string
		//: dirty changes the working tree before Head runs.
		dirty func(t *testing.T, root string)
		//: at is the directory Head is asked about, relative to the root.
		at           string
		wantModified bool
	}
	tests := []tc{
		{name: "a clean tree", dirty: func(*testing.T, string) {}},
		{name: "asked from a subdirectory", dirty: func(*testing.T, string) {}, at: "sub"},
		{
			name:         "an unstaged change to a tracked file",
			dirty:        func(t *testing.T, root string) { writeRepoFile(t, root, "tracked.go", "package p\n\nvar x int\n") },
			wantModified: true,
		},
		{
			name: "a staged change",
			dirty: func(t *testing.T, root string) {
				writeRepoFile(t, root, "staged.go", "package p\n")
				runGit(t, root, "add", "staged.go")
			},
			wantModified: true,
		},
		{
			//: an editor's swap file must not make a build stamp dirty.
			name:  "an untracked file only",
			dirty: func(t *testing.T, root string) { writeRepoFile(t, root, "scratch.txt", "notes\n") },
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		root := headRepo(t)
		c.dirty(t, root)
		head, headErr := gitpkg.Head(t.Context(), filepath.Join(root, c.at))
		if headErr != nil {
			t.Fatalf("Head() = %v, want nil", headErr)
		}
		if revision := runGit(t, root, "rev-parse", "HEAD"); head.Revision != revision {
			t.Errorf("Revision = %q, want %q", head.Revision, revision)
		}
		if !head.Time.Equal(want) {
			t.Errorf("Time = %v, want %v", head.Time, want)
		}
		//: the offset the commit was recorded in survives, as %cI prints it.
		if _, offset := head.Time.Zone(); offset != 2*60*60 {
			t.Errorf("Time is in offset %ds, want the recorded +02:00", offset)
		}
		if head.Modified != c.wantModified {
			t.Errorf("Modified = %v, want %v", head.Modified, c.wantModified)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestHeadRefusals pins the two refusals and what tells them apart. A caller
// describing a build goes without in both cases, but "there is no repository
// here" and "there is one and git would not answer" are different things to
// log, and an unborn branch is the second.
func TestHeadRefusals(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		dir      func(t *testing.T) string
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a directory outside any repository", dir: func(t *testing.T) string { return t.TempDir() }, wantCode: corevcs.CodeRepositoryUnresolved},
		{
			name:     "a directory that does not exist",
			dir:      func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") },
			wantCode: corevcs.CodeRepositoryUnresolved,
		},
		{
			name: "a repository with no commit yet",
			dir: func(t *testing.T) string {
				root := t.TempDir()
				runGit(t, root, "init", "-q")
				return root
			},
			wantCode: corevcs.CodeCommandFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		head, err := gitpkg.Head(t.Context(), c.dir(t))
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("Head() = %+v, %v; want code %v", head, err, c.wantCode)
		}
		if head != (gitpkg.HeadValue{}) {
			t.Errorf("a refusal carried a value: %+v", head)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
