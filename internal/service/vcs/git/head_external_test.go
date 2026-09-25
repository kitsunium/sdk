// External tests for head.go - git package.
package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestHeadRunsNoFilterTheRepositoryConfigures pins that asking a working tree
// whether it is modified never runs a command its .git/config names: git
// status passes a tracked file whose stat changed through the file's clean
// filter, and Head empties every configured driver before asking.
func TestHeadRunsNoFilterTheRepositoryConfigures(t *testing.T) {
	t.Parallel()
	//: the planted filter is a shell command; Windows' git runs it through its
	//: own sh, which this fixture does not depend on.
	if runtime.GOOS == "windows" {
		t.Skip("the planted filter is a POSIX shell command")
	}
	root := headRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, args...)...)
		//: a broken fixture must fail loudly rather than test nothing.
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	git("config", "filter.planted.clean", "touch '"+marker+"'; cat")
	writeRepoFile(t, root, ".gitattributes", "*.go filter=planted\n")
	git("add", ".gitattributes")
	git("commit", "-q", "-m", "attributes")
	//: the commit itself ran the filter; only Head's own runs matter.
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove marker: %v", err)
	}
	//: same content, new stat: git must look at the content to answer.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "tracked.go"), later, later); err != nil {
		t.Fatalf("touch: %v", err)
	}
	head, err := gitpkg.Head(t.Context(), root)
	if err != nil {
		t.Fatalf("Head() = %v", err)
	}
	//: the filter did not run.
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("Head ran the clean filter the repository's configuration names")
	}
	//: an unchanged content is not a modification.
	if head.Modified {
		t.Error("Modified = true for a file whose content did not change")
	}
}

// TestHeadCancelledIsNotUnresolved pins that a stopped question is not "no
// repository": the probe that tells the two apart would fail on the same
// cancelled context.
func TestHeadCancelledIsNotUnresolved(t *testing.T) {
	t.Parallel()
	root := headRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := gitpkg.Head(ctx, root)
	//: a failure, and not the unresolved one.
	if err == nil || errs.HasCode(err, corevcs.CodeRepositoryUnresolved) {
		t.Fatalf("Head(cancelled) = %v, want a failure that is not RepositoryUnresolved", err)
	}
	//: the context's own error is in the chain.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Head(cancelled) = %v, want context.Canceled in its chain", err)
	}
}
