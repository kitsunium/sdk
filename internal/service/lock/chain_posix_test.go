//go:build !windows

package lock_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as dirsafety_posix.go, because
// the rule it pins is that file's `plantable`. Windows answers the question
// differently and has its own file; see chain_windows_test.go.

// TestAnIndirectionAboveTheLockFileIsRefusedOnlyWhenAnyoneCouldHavePlantedIt
// pins the whole rule, including every accepting row.
//
// The rule is: a component of the lock directory's path that is an indirection
// is refused when the directory HOLDING it is world-writable, and accepted
// otherwise. The accepting rows are not decoration, and that is measured
// rather than argued: every path in this file sits under t.TempDir(), which on
// macOS is under /var — a symbolic link to /private/var that the operating
// system ships. These rows passed on e2e-cross's macos-arm64 job on the run
// that first exercised them, because the directory holding /var is / and
// nobody but root can write it. A guard that only ever refused would have
// refused every lock directory on that kernel and blamed the operator for
// Apple's layout. /var/run -> /run on most Linux distributions is the same
// shape.
//
// The sticky bit is deliberately NOT an exemption, which is the row that makes
// this rule different from checkDir's: sticky governs UNLINKING an entry that
// exists, and planting a component CREATES one at a name nobody has taken.
func TestAnIndirectionAboveTheLockFileIsRefusedOnlyWhenAnyoneCouldHavePlantedIt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		container fs.FileMode
		link      bool
		refuse    bool
	}
	tests := []tc{
		{"no indirection at all, owner-only container", 0o700, false, false},
		{"no indirection at all, world-writable container", 0o777, false, false},
		{"an indirection in an owner-only container", 0o700, true, false},
		{"an indirection in a group-writable container", 0o770, true, false},
		{"an indirection in a world-writable container", 0o777, true, true},
		{"an indirection in a world-writable STICKY container", 0o777 | os.ModeSticky, true, true},
		{"an indirection in a write-only-for-others container", 0o702, true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base := t.TempDir()
		//: the tree the link points at, and the one it would be a real
		//: directory in — identical in every way but the indirection, so the
		//: only difference between an accepted and a refused row is the thing
		//: under test.
		target := filepath.Join(base, "target")
		if err := os.MkdirAll(target, 0o700); err != nil {
			t.Fatalf("building the target = %v", err)
		}
		container := filepath.Join(base, "container")
		if err := os.Mkdir(container, 0o700); err != nil {
			t.Fatalf("building the container = %v", err)
		}
		middle := filepath.Join(container, "middle")
		//: the planted component, or an ordinary directory in its place.
		if c.link {
			if err := os.Symlink(target, middle); err != nil {
				t.Skipf("cannot create a symbolic link here: %v", err)
			}
		}
		if !c.link {
			if err := os.Mkdir(middle, 0o700); err != nil {
				t.Fatalf("building the middle directory = %v", err)
			}
		}
		//: the mode is set after everything is built, because Mkdir's mode is
		//: filtered by the umask and a runner with umask 022 would turn every
		//: world-writable row into 0755 and assert nothing.
		if err := os.Chmod(container, c.container); err != nil {
			t.Skipf("cannot set the mode this test needs (%v): %v", c.container, err)
		}
		dir := filepath.Join(middle, "locks")
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
		//: a platform without a native file lock refuses before it reads the
		//: path at all, so the skip cannot hide a wrong verdict.
		if errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Skip("no native file lock on this platform — the constructor refuses first")
		}
		//: the refusing half.
		if c.refuse {
			//: a constructor returning both a locker and an error is a
			//: different bug from one returning the wrong code, so both are
			//: asserted.
			if locker != nil {
				t.Fatalf("a link planted in a %v directory was accepted — every lock lands wherever it points", c.container)
			}
			if !errs.HasCode(err, svclock.CodeLockPathRedirected) {
				t.Fatalf("NewFileLocker under a planted %v container = %v, want LOCK_PATH_REDIRECTED", c.container, err)
			}
			//: and nothing was created inside the planted tree: the audit runs
			//: before MkdirAll, which would have followed the link.
			if _, statErr := os.Stat(filepath.Join(target, "locks")); !os.IsNotExist(statErr) {
				t.Fatalf("the refused construction still created a directory under the link's target")
			}
			//: verdict pinned.
			return
		}
		//: the accepting half, which is the one a careless rule loses.
		if err != nil || locker == nil {
			t.Fatalf("NewFileLocker under a %v container (link=%v) = %v, want a locker", c.container, c.link, err)
		}
	}
	//: one subtest per row, each on its own tree.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheChainAuditNamesTheComponentAndNotTheConfiguredDirectory pins the
// fields, because the component that was planted is usually neither the first
// nor the last thing an operator would look at.
func TestTheChainAuditNamesTheComponentAndNotTheConfiguredDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("building the target = %v", err)
	}
	container := filepath.Join(base, "pub")
	if err := os.Mkdir(container, 0o700); err != nil {
		t.Fatalf("building the container = %v", err)
	}
	planted := filepath.Join(container, "app")
	if err := os.Symlink(target, planted); err != nil {
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if err := os.Chmod(container, 0o777); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	dir := filepath.Join(planted, "locks")
	_, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("no native file lock on this platform")
	}
	fields := map[string]string{}
	//: the refusal's fields are the operator's whole diagnosis, so they are
	//: read as a map rather than matched against a rendered sentence that a
	//: future formatting change could reshape.
	for _, field := range errs.FieldsOf(err) {
		fields[field.Key()] = field.StringValue()
	}
	//: the component that redirects — usually neither the first nor the last
	//: thing an operator would have looked at. It is compared by IDENTITY and
	//: not by string, because it is reported where it LIVES and three
	//: platforms spell that three ways: macOS reaches t.TempDir() through
	//: /var -> /private/var, and Windows hands out TMP as an 8.3 short name.
	//: Both were measured on e2e-cross rather than anticipated.
	//:
	//: os.Lstat on both sides, so the planted link compares as ITSELF rather
	//: than being followed to the target it points at.
	reported, reportedErr := os.Lstat(fields["path"])
	if reportedErr != nil {
		t.Fatalf("Lstat(%s) = %v", fields["path"], reportedErr)
	}
	expected, expectedErr := os.Lstat(planted)
	if expectedErr != nil {
		t.Fatalf("Lstat(%s) = %v", planted, expectedErr)
	}
	if !os.SameFile(reported, expected) {
		t.Fatalf("field path = %q, which is not the planted component %q", fields["path"], planted)
	}
	//: the directory the caller configured, which is what they will grep their
	//: own configuration for.
	if fields["dir"] != dir {
		t.Fatalf("field dir = %q, want %q", fields["dir"], dir)
	}
	//: and where the indirection actually goes.
	if fields["target"] != target {
		t.Fatalf("field target = %q, want %q", fields["target"], target)
	}
}
