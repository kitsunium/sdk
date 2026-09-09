// Package lock — the lease a holder gets back, and the sibling interface a
// lease uses to say whether it can expire at all. Kept out of lock.go so the
// FROZEN [Locker] port stands alone in its own file.
package lock

import (
	"context"
	"time"
)

// Lease is a held lock. It is what [Locker.Acquire] returns and the ONLY
// handle through which the lock can be renewed or released.
//
// The method set is FROZEN at three under ADR 0039, for the same reason
// [Locker] is: pkg/v1 aliases it, Go interfaces are structural, and a fourth
// method would break every downstream double at compile time. [Deadliner] is
// the sibling that ships.
//
// A Lease is safe for concurrent use, but it is not a value to pass around
// casually: it carries the holder's identity, and whoever holds it can release
// the lock.
type Lease interface {
	// Fence returns this acquisition's fencing token: a uint64 that strictly
	// increases with every successful acquisition of the same name, whoever
	// acquires it.
	//
	// It is the only mechanism in this domain that survives a stalled holder,
	// and it only works if the PROTECTED RESOURCE compares it and refuses the
	// lower one. Handing it to a resource that ignores it buys nothing; see
	// the package comment for what is and is not guaranteed as a result.
	//
	// Fence never returns 0 for a live lease. Zero is the "no acquisition"
	// value, so a caller that forgets to plumb the token through cannot have
	// it silently compare equal to a legitimate one.
	Fence() uint64
	// Extend renews the lease for another full lifetime, starting now.
	//
	// It returns [LockNotHeld] when this holder no longer owns the lock —
	// which is not a warning to log and continue past. It means another holder
	// is, or soon will be, inside the section this caller believes it owns.
	// The only correct response is to stop the protected work.
	//
	// On an implementation whose leases cannot expire, Extend re-asserts
	// ownership and succeeds; it is never a silent no-op that hides a lost
	// lock, because there is no way for such a lock to be lost while held.
	Extend(ctx context.Context) error
	// Release gives the lock up. It releases ONLY a lock this holder still
	// holds: a lease that was taken over after expiring releases nothing and
	// reports [LockNotHeld], because unlocking a section another holder is
	// inside would be worse than leaking the lock.
	//
	// Releasing a lease this holder already released is not an error — the
	// caller's intent is satisfied — so `defer lease.Release(ctx)` next to an
	// explicit release is safe.
	Release(ctx context.Context) error
}

// Deadliner is the sibling interface (ADR 0039) implemented by a [Lease] that
// CAN expire. Reach it by type assertion:
//
//	if d, ok := lease.(lock.Deadliner); ok {
//	    // this lease has a deadline, and can be taken from us at it
//	}
//
// The assertion is the answer to the only question that changes how a caller
// must be written. A lease that implements Deadliner can be taken away from a
// live holder, so its work must either finish before the deadline, renew, or
// carry the fence through to the resource. A lease that does NOT implement it
// cannot be taken away at all, so none of that is needed — and a caller can
// tell which world it is in without naming an implementation.
//
// This is deliberately not a Deadline method on [Lease] returning a zero time
// for "never". A zero time.Time is a value someone will compare against
// time.Now and lose to; an absent interface is not.
type Deadliner interface {
	// Deadline returns the instant at which this lease stops being held
	// unless it is renewed first. It moves forward on every successful
	// [Lease.Extend].
	Deadline() time.Time
}
