// Package lock — the lease handed out by the file locker. It carries no
// deadline, deliberately: see [fileLocker] for why its locks do not expire.
package lock

import (
	"context"
	"os"
	"sync"
)

// fileLease is one held flock, plus the gate entry that guards it from the
// goroutines of this process.
//
// It does NOT implement [github.com/kitsunium/sdk/internal/core/lock.Deadliner],
// and the absence is load-bearing: a caller that type-asserts for Deadliner
// and finds nothing has been told, by the API rather than by documentation,
// that this lock cannot be taken from it while it lives.
type fileLease struct {
	// locker is where the descriptor and the gate entry are given back.
	locker *fileLocker
	// file is the open description the flock is held on. Its identity IS the
	// lock: closing it releases, and nothing else can.
	file *os.File
	// name is the lock this lease was taken on, needed to release the gate.
	name string
	// fence is the acquisition's fencing token, read from and written to the
	// lock file under this very flock.
	fence uint64
	// mu guards released. It is an RWMutex because Extend only reads it —
	// a keepalive calls Extend on a cadence, and a read lock keeps that off
	// the path Release contends for.
	mu sync.RWMutex
	// released records that this holder already gave the lock back, so a
	// second Release is a no-op rather than a double close of a descriptor
	// another lease may by then have reopened.
	released bool
}

// newFileLease binds a lease to a held flock.
func newFileLease(locker *fileLocker, file *os.File, name string, fence uint64) *fileLease {
	//: everything the lease needs to release is captured here.
	return &fileLease{locker: locker, file: file, name: name, fence: fence}
}

// Fence returns this acquisition's fencing token.
//
// Unlike the in-process locker's, this counter is PERSISTENT: it lives in the
// lock file and survives every process that ever held the lock, so a token
// issued after a reboot is still greater than every token issued before it.
func (l *fileLease) Fence() uint64 {
	//: immutable for the life of the lease.
	return l.fence
}

// Extend re-asserts ownership.
//
// It cannot fail for a lease this process still holds, and that is not a
// weakness disguised as a guarantee — it follows from the lock having no
// expiry at all. Nothing can take a held flock away: not a timer, not another
// process, not another goroutine (the gate). So there is no state in which
// this lease is lost while the holder is running, and reporting success is the
// truth rather than a silent no-op.
//
// It still returns [corelock.LockNotHeld] after Release, because at that point
// the lease genuinely holds nothing.
func (l *fileLease) Extend(ctx context.Context) error {
	//: an abandoned caller is told so rather than reassured about a lock it is
	//: no longer using.
	if ctx.Err() != nil {
		//: the caller's own error.
		return ctx.Err()
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	//: a released lease holds nothing; saying otherwise would let a caller
	//: believe it re-entered a section it had left.
	if l.released {
		//: LOCK_NOT_HELD, naming the operation.
		return notHeld("Extend", l.name, l.fence)
	}
	//: still held, and nothing could have taken it.
	return nil
}

// Release gives the lock back.
//
// Like the in-process lease it deliberately IGNORES ctx: the common call site
// is `defer lease.Release(ctx)` with the context whose cancellation ended the
// work, and honouring it would mean a cancelled operation never closes its
// descriptor. Here the consequence is worse than a leaked lease — the flock
// would be held until the process exits, blocking every other process too.
func (l *fileLease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	//: releasing twice is not an error: the caller wanted the lock gone and it
	//: is gone. It also must not close the descriptor twice.
	if l.released {
		//: intent already satisfied.
		return nil
	}
	//: mark first so no path can reach the descriptor again.
	l.released = true
	//: flock, then descriptor, then gate — see fileLocker.release.
	return l.locker.release(l.file, l.name)
}
