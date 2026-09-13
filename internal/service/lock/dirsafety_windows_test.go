//go:build windows

package lock_test

import (
	"os"
	"path/filepath"
	"testing"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as dirsafety_windows.go. It
// holds the measurement that decided the Windows directory verdict and the
// pins for that verdict; the POSIX rule and its table live in
// dirsafety_posix_test.go.

// TestThePosixDirectoryRuleWouldRefuseEveryDirectory is the measurement behind
// the build-tag split, and the reason the rule could not simply be left alone.
//
// The POSIX rule is "refuse when the other-write bit is set AND the sticky bit
// is not". On Windows os.Stat does not read permission bits — there are none
// to read — it SYNTHESISES them from the single FILE_ATTRIBUTE_READONLY flag:
// 0444 when the flag is set, 0666 when it is not, plus ModeDir|0111 for a
// directory. Every writable directory on the system therefore reports 0777
// with no sticky bit, and the predicate is true for all of them.
//
// Running the rule here would make NewFileLocker refuse every directory a
// caller could name — the failure mode ADR 0018 §(a) exists to prevent, and
// one that would arrive as a typed LOCK_DIRECTORY_UNSAFE that looks like a
// deployment fault rather than a bug in this package.
func TestThePosixDirectoryRuleWouldRefuseEveryDirectory(t *testing.T) {
	t.Parallel()
	owned := filepath.Join(t.TempDir(), "locks")
	//: 0700 is the mode NewFileLocker itself creates a lock directory with, so
	//: this is the most favourable input the rule could be given.
	if err := os.Mkdir(owned, 0o700); err != nil {
		t.Fatalf("creating the lock directory = %v", err)
	}
	for _, dir := range []string{owned, t.TempDir(), os.TempDir()} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("os.Stat(%q) = %v", dir, err)
		}
		mode := info.Mode()
		if mode.Perm() != 0o777 {
			t.Fatalf("os.Stat(%q).Mode().Perm() = %v, want 0777 — this platform's synthesised bits have changed and the split's justification with them", dir, mode.Perm())
		}
		if mode&os.ModeSticky != 0 {
			t.Fatalf("os.Stat(%q).Mode() = %v, want no sticky bit", dir, mode)
		}
		//: the POSIX predicate, transcribed rather than called: the production
		//: copy is behind the other half of the build tag and is unreachable
		//: from here, which is exactly the point of the split.
		if refused := mode&0o002 != 0 && mode&os.ModeSticky == 0; !refused {
			t.Fatalf("the POSIX rule accepted %q (mode %v) — the measurement this split rests on no longer holds", dir, mode)
		}
	}
}

// TestNewFileLockerAcceptsADirectoryTheModeBitsCallWorldWritable pins the
// Windows verdict: the directory check accepts, and says why rather than
// pretending to have checked something.
//
// The case that matters is the SECOND run, not the first. prepareDir checks
// only directories it did not create, so a locker that creates its own
// directory would work once and refuse on every later start — an intermittent
// failure that reproduces only on a machine that has already run the program.
func TestNewFileLockerAcceptsADirectoryTheModeBitsCallWorldWritable(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "locks")

	first, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if err != nil || first == nil {
		t.Fatalf("NewFileLocker on a fresh directory = %v, want a locker", err)
	}
	//: the same path, now existing — the branch that runs checkDir.
	second, secondErr := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	if secondErr != nil || second == nil {
		t.Fatalf("NewFileLocker on an existing directory = %v, want a locker — the POSIX rule is running on synthesised mode bits and refuses every directory after the first start", secondErr)
	}
}

// TestTheLockFileCannotBeUnlinkedOrRenamedWhileItIsOpen is the evidence the
// Windows directory verdict rests on.
//
// What the POSIX rule prevents is not a leaked counter, it is an INODE SWAP:
// an account that can unlink the lock file replaces it, the next process locks
// the NEW file while the holder still locks the old one, and both are told
// they hold the same lock. No race is required, and on Unix nothing but the
// directory's permissions stands in the way.
//
// On Windows the kernel stands in the way instead. os.OpenFile reaches
// CreateFileW with FILE_SHARE_READ|FILE_SHARE_WRITE and deliberately WITHOUT
// FILE_SHARE_DELETE (Go 1.27, src/syscall/syscall_windows.go, func Open), so
// while any holder has the lock file open neither a delete nor a rename can
// touch it — whatever the directory's ACL says. The swap is refused by the
// open, not by the permissions, which is why the permissions are not consulted.
//
// If this ever starts passing the deletion, the Windows verdict loses its
// justification and the gap in the platform matrix becomes a real exposure.
func TestTheLockFileCannotBeUnlinkedOrRenamedWhileItIsOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := lockFilePath(dir, "job")
	file := openLockFile(t, path)

	if err := os.Remove(path); err == nil {
		t.Fatal("the lock file was deleted while a holder had it open — the inode swap the POSIX rule refuses is reachable here, and the Windows directory verdict has lost its justification")
	}
	if err := os.Rename(path, filepath.Join(dir, "moved.lock")); err == nil {
		t.Fatal("the lock file was renamed away while a holder had it open — the same swap by another name")
	}
	//: and once nobody holds it, it is an ordinary file again: the guarantee
	//: is about a held lock, not about the directory, and the test says which.
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("closing the lock file = %v", closeErr)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("removing the closed lock file = %v, want it to succeed", err)
	}
}
