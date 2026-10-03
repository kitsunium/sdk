//go:build windows

package lock_test

import (
	"os"
	"path/filepath"
	"testing"
)

// This file is the Windows half of identity_posix.go's subject. The exposure
// it answers — a lock file unlinked out from under its holder — has no
// mechanism here, and that is asserted rather than assumed.
//
// The lane that executes it is the `windows` job of
// .github/workflows/e2e-cross.yml.

// TestExtendKeepsSucceedingOnAHeldLock is the measurement this change most
// needs from a real Windows kernel.
//
// Extend now compares the descriptor's identity against the path, through
// os.SameFile. On Windows os.SameFile loads a file index by opening the PATH,
// with a desired access of zero — a request Windows does not subject to the
// sharing check — while the lock file is already open by this process WITHOUT
// FILE_SHARE_DELETE and carries a mandatory LockFileEx range over its whole
// length (ADR 0081).
//
// If any of that were wrong, os.SameFile would answer "not the same file" for
// every held lock and EVERY renewal on this platform would fail. That is a
// far worse outcome than the exposure the check exists for, so it is pinned
// here, on the only lane that runs a Windows kernel, rather than reasoned
// about from documentation.
func TestExtendKeepsSucceedingOnAHeldLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := newFileLocker(t, dir)
	lease, err := locker.Acquire(t.Context(), "shared")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	releaseAtEnd(t, lease)
	//: three renewals, because a check that consumed something on its first
	//: call would pass a single assertion and fail a keepalive's second tick.
	for attempt := range 3 {
		if extendErr := lease.Extend(t.Context()); extendErr != nil {
			t.Fatalf("Extend #%d on a held lock = %v, want nil — os.SameFile cannot describe a file this process holds without FILE_SHARE_DELETE", attempt+1, extendErr)
		}
	}
}

// TestAHeldLockFileCannotBeSwappedOnWindows pins why identity_posix_test.go's
// table has no counterpart here: the operating system refuses the move.
//
// os.OpenFile reaches CreateFileW with FILE_SHARE_READ|FILE_SHARE_WRITE and
// deliberately without FILE_SHARE_DELETE (go1.27.0,
// src/syscall/syscall_windows.go, func Open), so a held lock file can be
// neither unlinked nor renamed whatever the directory's ACL permits. The
// exposure therefore has no mechanism on this platform — and if a future Go
// release adds the share flag, this test is what says so.
func TestAHeldLockFileCannotBeSwappedOnWindows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	locker := newFileLocker(t, dir)
	lease, err := locker.Acquire(t.Context(), "shared")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	releaseAtEnd(t, lease)
	path := lockFilePath(dir, "shared")
	//: the unlink half.
	if removeErr := os.Remove(path); removeErr == nil {
		t.Fatalf("removing a held lock file succeeded; the identity check is now the ONLY guard on this platform and identity_posix_test.go's table belongs here too")
	}
	//: and the rename half, which is the variant an unlink check alone would
	//: miss.
	if renameErr := os.Rename(path, filepath.Join(dir, "moved.lock")); renameErr == nil {
		t.Fatalf("renaming a held lock file succeeded; see the unlink case above")
	}
}
