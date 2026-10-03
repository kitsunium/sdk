//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package flock — flock(2), on the kernels that have it.
//
// flock(2) is not POSIX. It exists on Linux and on every BSD (macOS included)
// with the same shape, and the tag set above is exactly where Go's syscall
// package declares it — android and ios reach this file through the linux and
// darwin tags. The lock belongs to the open file DESCRIPTION: a dup(2)'d
// descriptor shares it, a second open(2) of the same path does not, and the
// kernel drops it when the last descriptor on the description closes — which a
// process's death does. It is ADVISORY: it binds every process that takes it,
// and nothing else.
package flock

import (
	"errors"
	"os"
	"syscall"
)

// Native reports that this GOOS has a file lock: flock(2), here.
const Native bool = true

// tryLock is [TryLock] over flock(LOCK_EX|LOCK_NB).
//
// It is deliberately non-blocking — see the package comment for what a
// blocking LOCK_EX costs a caller with a deadline.
func tryLock(file *os.File) (held bool, err error) {
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

// unlock is [Unlock] over flock(LOCK_UN), on the descriptor the lock was taken
// through.
func unlock(file *os.File) error {
	//: LOCK_UN on the same descriptor the lock was taken on.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
