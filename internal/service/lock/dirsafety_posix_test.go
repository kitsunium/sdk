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

// This file carries the SAME build constraint as dirsafety_posix.go, so it
// runs everywhere the POSIX directory rule is compiled and nowhere it is not.
// Windows reaches a different verdict for reasons of its own and has its own
// file; see dirsafety_windows_test.go.

// makeDir creates a child of t.TempDir() with mode and returns its path.
//
// A child rather than the temp directory itself, so t.TempDir()'s own cleanup
// never has to remove a directory this test just made hostile.
func makeDir(t *testing.T, mode fs.FileMode) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "locks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating the lock directory = %v", err)
	}
	//: Mkdir's mode is filtered by the umask, so the mode under test is set
	//: afterwards — otherwise a runner with umask 022 would silently turn
	//: every group- and other-writable case into 0755 and the table would
	//: assert nothing.
	if err := os.Chmod(dir, mode); err != nil {
		t.Skipf("cannot set the mode this test needs (%v): %v", mode, err)
	}
	return dir
}

// TestTheDirectoryRuleIsOtherWriteAndNotSticky pins the whole POSIX rule, not
// just its two extremes.
//
// The rule is: refuse when the other-write bit is set AND the sticky bit is
// not. Every other combination is accepted, and each acceptance is a decision
// rather than an oversight — group-writable is how two service accounts share
// a lock deliberately, and world-writable WITH sticky is exactly what /tmp is,
// where only an entry's owner may unlink it.
//
// The table exists because this rule is about to be split by build tag. A
// split guarded only by the world-writable case can preserve "refuses the
// obvious one" while dropping "accepts the deliberate ones", and nothing would
// say so.
func TestTheDirectoryRuleIsOtherWriteAndNotSticky(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		mode   fs.FileMode
		refuse bool
	}
	tests := []tc{
		{"owner-only", 0o700, false},
		{"group-writable", 0o770, false},
		{"group-readable", 0o750, false},
		{"world-writable without the sticky bit", 0o777, true},
		{"world-writable with the sticky bit", 0o777 | os.ModeSticky, false},
		{"world-writable with no group access", 0o707, true},
		{"world-WRITE only", 0o702, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := makeDir(t, c.mode)
		//: a filesystem that drops the sticky bit would turn the accepted case
		//: into a refused one for a reason that is not this rule.
		if c.mode&os.ModeSticky != 0 {
			info, statErr := os.Stat(dir)
			if statErr != nil || info.Mode()&os.ModeSticky == 0 {
				t.Skip("this filesystem does not honour the sticky bit on a directory")
			}
		}
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
		//: a platform without a native file lock refuses before it ever reads
		//: the mode, so the skip cannot hide a wrong verdict.
		if errors.Is(err, coreproc.UnsupportedPlatform) {
			t.Skip("no native file lock on this platform — the constructor refuses first")
		}
		//: the refusing half of the rule.
		if c.refuse {
			if locker != nil {
				t.Fatalf("mode %v was accepted — its lock files can be replaced by any account", c.mode)
			}
			if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
				t.Fatalf("NewFileLocker on %v = %v, want LOCK_DIRECTORY_UNSAFE", c.mode, err)
			}
			//: verdict pinned.
			return
		}
		//: the accepting half, which is the one a careless split loses.
		if err != nil || locker == nil {
			t.Fatalf("NewFileLocker on %v = %v, want a locker", c.mode, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNewFileLockerRefusesAWorldWritableDirectory pins the permission rule and
// the attack behind it: an account that can unlink the lock file replaces its
// INODE, after which two processes lock two different files and both are told
// they hold the same lock.
func TestNewFileLockerRefusesAWorldWritableDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("no flock(2) on this platform")
	}
	if locker != nil {
		t.Fatal("a world-writable, non-sticky lock directory was accepted")
	}
	if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
		t.Fatalf("NewFileLocker = %v, want LOCK_DIRECTORY_UNSAFE", err)
	}
}

// TestAStickyWorldWritableDirectoryIsAccepted is the other half of the rule.
// /tmp is 1777, and the sticky bit is precisely what makes it safe: only an
// entry's owner may unlink it. Refusing it would push callers somewhere worse.
func TestAStickyWorldWritableDirectoryIsAccepted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777|os.ModeSticky); err != nil {
		t.Skipf("cannot set the mode this test needs: %v", err)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || info.Mode()&os.ModeSticky == 0 {
		t.Skip("this filesystem does not honour the sticky bit on a directory")
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Skip("no flock(2) on this platform")
	}
	if err != nil || locker == nil {
		t.Fatalf("NewFileLocker on a sticky directory = %v, want a locker", err)
	}
}
