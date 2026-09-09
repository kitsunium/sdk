// Package lock — the lease handed out by the in-process locker. It is the
// only backend whose leases satisfy corelock.Deadliner, because it is the only
// one whose leases can be taken from a live holder.
package lock

import (
	"context"
	"sync"
	"time"
)

// memoryLease is one acquisition, held by the caller that took it.
//
// It carries the holder token rather than a pointer to the holding: the
// holding it refers to may already have been taken over, and the whole point
// of Release is to notice that instead of unlocking whatever is there now.
type memoryLease struct {
	// locker is where the token is checked. A lease is useless without it.
	locker *memoryLocker
	// name is the lock this lease was taken on.
	name string
	// token identifies THIS acquisition. It never changes.
	token uint64
	// fence is the fencing token for this acquisition. It never changes:
	// renewing a lease does not reorder it against other holders, so a
	// renewal that moved the fence would invalidate the caller's own earlier
	// writes at the resource.
	fence uint64
	// mu guards the two fields below, which move. It is an RWMutex because
	// Deadline reads without writing, and a lease's deadline is polled by
	// anything scheduling its own renewal.
	mu sync.RWMutex
	// deadline is the last deadline this lease knows about. Extend moves it.
	deadline time.Time
	// released records that this holder already gave the lock back, so a
	// second Release reports success rather than an alarming LOCK_NOT_HELD
	// that only means "you did this twice".
	released bool
}

// newMemoryLease binds a lease to a fresh holding.
func newMemoryLease(locker *memoryLocker, name string, held *holding) *memoryLease {
	//: everything the lease needs is copied out under the locker's mutex by
	//: the caller; the holding itself is deliberately not retained.
	return &memoryLease{
		locker:   locker,
		name:     name,
		token:    held.token,
		fence:    held.fence,
		deadline: held.deadline,
	}
}

// Fence returns this acquisition's fencing token.
func (l *memoryLease) Fence() uint64 {
	//: immutable for the life of the lease — no lock needed, and no renewal
	//: moves it.
	return l.fence
}

// Deadline returns the instant this lease stops being held unless renewed.
//
// Its presence is the signal: a lease that implements the domain's Deadliner
// sibling can be taken from a live holder, and a caller that type-asserts for
// it is asking exactly the question that changes how it must be written.
func (l *memoryLease) Deadline() time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	//: the deadline as of the last successful Extend, or the acquisition.
	return l.deadline
}

// Extend renews the lease for another full lifetime.
//
// A cancelled ctx is reported before the attempt, because renewing a lease for
// work that has already been abandoned would hold the lock for a lifetime
// nobody is using.
func (l *memoryLease) Extend(ctx context.Context) error {
	//: an abandoned caller does not get its lease extended.
	if ctx.Err() != nil {
		//: the caller's own error.
		return ctx.Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	//: a released lease is not renewable; saying otherwise would let a caller
	//: believe it re-entered a section it had left.
	if l.released {
		//: LOCK_NOT_HELD, and it names the operation.
		return notHeld("Extend", l.name, l.token)
	}
	deadline, err := l.locker.extend(l.name, l.token)
	//: the lock was taken over, or the lease had lapsed.
	if err != nil {
		//: propagate untouched — origin wins (ADR 0005).
		return err
	}
	//: record the new deadline for Deadline().
	l.deadline = deadline
	//: renewed.
	return nil
}

// Release gives the lock back.
//
// It deliberately IGNORES ctx. The overwhelmingly common call site is
// `defer lease.Release(ctx)` with the very context whose cancellation ended
// the work, so honouring it would mean a cancelled operation never releases
// its lock — every timeout would leak a lease for a full TTL, and the leak
// would look like contention rather than like a bug.
func (l *memoryLease) Release(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	//: releasing twice is not an error: the caller wanted the lock gone and
	//: it is gone. This is what makes a defer next to an explicit release safe.
	if l.released {
		//: intent already satisfied.
		return nil
	}
	//: mark first: even if the locker reports the lock was taken over, this
	//: lease is spent and must not release again later.
	l.released = true
	//: the token check happens in the locker, under its mutex.
	return l.locker.release(l.name, l.token)
}
