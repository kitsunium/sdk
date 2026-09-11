//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session — the two operating-system mechanics the file store needs,
// on the platforms that have them.
//
// The file store rests on three guarantees, and only two of them are portable.
// Atomic publication is rename(2), which POSIX requires to be atomic and which
// Go's os.Rename also provides on Windows through MoveFileEx. Restrictive
// permissions and advisory locking are not: they are the reason this file has a
// build tag and a sibling that refuses.
package session

import (
	"errors"
	"os"
	"syscall"
)

// platformNative reports that this GOOS has both mechanics natively. The file
// store's constructor reads it and refuses on the platforms where it is false,
// rather than building a store that would silently provide neither.
const platformNative bool = true

// tryLockExclusive attempts an exclusive advisory lock on an open descriptor
// WITHOUT waiting, reporting whether it got one.
//
// flock(2) is per-open-file-description, so the descriptor the store holds for
// its whole lifetime is the lock, and no other process can hold it at the same
// time. It is ADVISORY: it binds every process that takes it, which is every
// process using this store, and binds nothing else. A mandatory lock would need
// a mount option no portable code can require.
//
// It is LOCK_NB, and the store polls, because a blocking LOCK_EX parks the
// thread inside a syscall no cancellation can reach: a caller whose request was
// abandoned would keep waiting for a lock it no longer has any use for, and the
// goroutine would not come back until some other process released it. The same
// decision internal/service/lock made for the same syscall (ADR 0052), now the
// same here (ADR 0073).
func tryLockExclusive(file *os.File) (taken bool, err error) {
	flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	//: ours.
	if flockErr == nil {
		//: acquired.
		return true, nil
	}
	//: held elsewhere — the one refusal that is not a fault. EAGAIN and
	//: EWOULDBLOCK are the same value on Linux and differ on some BSDs, so
	//: both are read.
	if errors.Is(flockErr, syscall.EWOULDBLOCK) || errors.Is(flockErr, syscall.EAGAIN) {
		//: the caller waits and tries again.
		return false, nil
	}
	//: anything else is the call itself failing.
	return false, flockErr
}

// unlockFile releases the advisory lock taken by [tryLockExclusive].
func unlockFile(file *os.File) error {
	//: closing the descriptor would also release it; unlocking explicitly keeps
	//: the descriptor alive for the next operation.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
