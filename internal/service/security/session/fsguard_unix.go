//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package session — the operating-system mechanics the file store needs, on
// the platforms that have them.
//
// The file store rests on three guarantees, and only two of them are portable.
// Atomic publication is rename(2), which POSIX requires to be atomic and which
// Go's os.Rename also provides on Windows through MoveFileEx. Restrictive
// permissions and advisory locking are not: they are the reason this file has a
// build tag and a sibling that refuses. So is the question [plantable]
// answers, which a mode can answer here and cannot on Windows.
package session

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// platformNative reports that this GOOS has both mechanics natively. The file
// store's constructor reads it and refuses on the platforms where it is false,
// rather than building a store that would silently provide neither.
const platformNative bool = true

// worldWritable is the permission bit that lets any account create an entry in
// a directory.
const worldWritable fs.FileMode = 0o002

// plantable reports whether any account could have created an entry in a
// directory whose mode is container, and renders what it read.
//
// It is internal/service/app/lock's rule for the same question (ADR 0083), read
// off the same pathchain.StepValue.Container. Other-write is the one bit that
// answers it. Group-write is not enough: a directory shared with a group is a
// deliberate arrangement, and its members are accounts the operator chose. The
// sticky bit is deliberately NOT consulted: it says only an entry's owner may
// UNLINK it, and planting a component CREATES an entry at a name nobody has
// taken, so 0777|sticky — exactly what /tmp is — is plantable.
func plantable(container fs.FileMode) (yes bool, observed string) {
	//: nobody outside the owner and the group can create an entry here.
	if container&worldWritable == 0 {
		//: a verdict, and it is "safe"; observed stays empty.
		return false, ""
	}
	//: plantable, and the mode is what an operator has to change.
	return true, "container=" + container.Perm().String()
}

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
// decision internal/service/app/lock made for the same syscall (ADR 0052), now the
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
