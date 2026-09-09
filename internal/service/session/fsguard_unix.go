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
	"os"
	"syscall"
)

// platformNative reports that this GOOS has both mechanics natively. The file
// store's constructor reads it and refuses on the platforms where it is false,
// rather than building a store that would silently provide neither.
const platformNative bool = true

// lockExclusive takes an exclusive advisory lock on an open descriptor and
// blocks until it has one.
//
// flock(2) is per-open-file-description, so the descriptor the store holds for
// its whole lifetime is the lock, and no other process can hold it at the same
// time. It is ADVISORY: it binds every process that takes it, which is every
// process using this store, and binds nothing else. A mandatory lock would need
// a mount option no portable code can require.
func lockExclusive(file *os.File) error {
	//: LOCK_EX without LOCK_NB — an operation waits its turn rather than
	//: failing, because the alternative is a caller retry loop around a lock
	//: that is held for microseconds.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

// unlockFile releases the advisory lock taken by [lockExclusive].
func unlockFile(file *os.File) error {
	//: closing the descriptor would also release it; unlocking explicitly keeps
	//: the descriptor alive for the next operation.
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
