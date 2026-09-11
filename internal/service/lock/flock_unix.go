//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package lock — the one operating-system mechanic the file locker needs, on
// the platforms that have it.
//
// flock(2) is not POSIX. It exists on Linux and on every BSD (macOS included)
// with the same shape, and Windows has no equivalent with the same semantics:
// LockFileEx locks BYTE RANGES of a file and its locks are mandatory rather
// than advisory, which is a different contract that would need a different
// implementation and a different set of tests. Hence the build tag and the
// sibling that refuses (ADR 0018).
package lock

import (
	"errors"
	"os"
	"syscall"
)

// platformNative reports that this GOOS has flock(2) natively. The file
// locker's constructor reads it and refuses where it is false, rather than
// building a locker that would report success and exclude nothing.
const platformNative bool = true

// flockTry takes an exclusive advisory lock on file without blocking. It
// reports (true, nil) when the lock is now held, (false, nil) when it is held
// elsewhere, and (false, err) when the call itself failed.
//
// It is deliberately non-blocking. A blocking flock(2) parks the calling
// thread inside a syscall that no context cancellation can reach, so an
// Acquire with a deadline would ignore it; the poll loop above this function
// is what makes the caller's ctx mean something.
func flockTry(file *os.File) (held bool, err error) {
	flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	//: the lock is ours.
	if flockErr == nil {
		//: acquired.
		return true, nil
	}
	//: EWOULDBLOCK is the ordinary "someone else has it" answer, not a fault.
	//: On Linux EAGAIN and EWOULDBLOCK are the same value; both are checked
	//: because the BSDs are not required to agree.
	if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
		//: held elsewhere.
		return false, nil
	}
	//: anything else is a real failure of the call.
	return false, flockErr
}

// flockUnlock releases the advisory lock taken by [flockTry].
//
// Closing the descriptor would also release it, and the file locker closes it
// immediately afterwards. Unlocking explicitly first keeps the two events
// ordered on purpose: the lock is given up while the descriptor is still
// valid, so a failure to release is reportable rather than swallowed by a
// close that "worked".
func flockUnlock(file *os.File) error {
	//: LOCK_UN on the same descriptor the lock was taken on.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
