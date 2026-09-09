// Package lock — the in-process locker: real leases, real expiry, real
// takeover, and therefore the backend where fencing actually matters.
package lock

import (
	"context"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
)

// initialNames is the capacity hint for both per-name maps. A process
// typically guards a handful of named resources, not thousands; the hint costs
// one small allocation and saves the first few rehashes.
const initialNames int = 8

// memoryLocker is the in-process [corelock.Locker].
//
// # Why THIS backend needs a TTL and the file one does not
//
// A goroutine that stops — panics behind a recover, blocks on a channel
// nobody will ever send to, leaks — leaves no trace the runtime will act on.
// Nothing releases its lock. Without expiry, one such goroutine deadlocks
// every future caller of that name for the life of the process, and the only
// evidence is a hang. So this locker expires leases, which means it can also
// TAKE ONE AWAY from a holder that is still running — the hazard the domain's
// package comment describes at length, and the reason [corelock.Lease.Fence]
// exists.
type memoryLocker struct {
	// mu guards every map and every holding in them.
	mu sync.Mutex
	// ttl is the validated, strictly positive lease lifetime.
	ttl time.Duration
	// clk is the time source: it reads Now for expiry and arms a timer to a
	// holder's deadline so a waiter wakes exactly then instead of polling.
	clk clock.Timed
	// held maps a name to its live acquisition, if any.
	held map[string]*holding
	// fences maps a name to the highest fencing token ever issued for it.
	//
	// It is NEVER pruned, and that is deliberate: forgetting a name's counter
	// resets it, and a reset fencing token reissues numbers the protected
	// resource has already accepted. The cost is one map entry per distinct
	// name for the process's lifetime, which is stated in this package's
	// CLAUDE.md as a bound on the names a caller should mint.
	fences map[string]uint64
	// nextToken is the source of holder identities, monotone per locker.
	nextToken uint64
}

// NewMemory returns a [corelock.Locker] whose leases live in this process.
//
// It refuses a non-positive cfg.TTL at CONSTRUCTION rather than picking a
// default. Both readings of a zero TTL are defensible and they are opposites:
// "already expired" makes every Acquire succeed while excluding nobody, and
// "never expires" turns a leaked goroutine into a permanent deadlock. An SDK
// that guessed would be silently wrong for half its callers, so it does not
// guess (ADR 0031).
func NewMemory(cfg MemoryConfig) (locker corelock.Locker, err error) {
	//: the TTL is checked before anything is allocated.
	if invalid := cfg.validate(); invalid != nil {
		//: LockMisconfigured, naming the field.
		return nil, invalid
	}
	//: usable configuration — build the locker.
	return &memoryLocker{
		ttl:    cfg.TTL,
		clk:    cfg.clockOrSystem(),
		held:   make(map[string]*holding, initialNames),
		fences: make(map[string]uint64, initialNames),
	}, nil
}

// Acquire blocks until name is held by this caller or ctx ends.
func (l *memoryLocker) Acquire(ctx context.Context, name string) (lease corelock.Lease, err error) {
	//: an empty name is what an uninitialised variable holds; see errors.go.
	if rejected := checkName(name); rejected != nil {
		//: LockNameRejected.
		return nil, rejected
	}
	//: contend, sleep until something changes, contend again.
	for {
		//: a cancellation observed before contending saves a pointless attempt.
		if ctx.Err() != nil {
			//: the caller's own deadline, reported as the caller's own error.
			return nil, ctx.Err()
		}
		taken, free, until := l.attempt(name)
		//: attempt returns a lease when the name was free or expired.
		if taken != nil {
			//: held.
			return taken, nil
		}
		//: held elsewhere — wait for the holder to end or for its deadline.
		if waitErr := l.await(ctx, free, until); waitErr != nil {
			//: only ctx can end the wait unsuccessfully.
			return nil, waitErr
		}
	}
}

// TryAcquire attempts the acquisition once and never waits.
func (l *memoryLocker) TryAcquire(ctx context.Context, name string) (lease corelock.Lease, held bool, err error) {
	//: same refusal as Acquire, for the same reason.
	if rejected := checkName(name); rejected != nil {
		//: LockNameRejected.
		return nil, false, rejected
	}
	//: a cancelled caller gets its own error rather than a lock it cannot use.
	if ctx.Err() != nil {
		//: the caller's deadline, reported as the caller's error.
		return nil, false, ctx.Err()
	}
	taken, _, _ := l.attempt(name)
	//: "held elsewhere" is not a failure: the caller offered to give up at
	//: once and the offer was accepted.
	return taken, taken != nil, nil
}

// attempt takes name if it is free or expired. On failure it returns the
// current holding's free channel and deadline so the caller can wait on the
// two events that could change the answer.
func (l *memoryLocker) attempt(name string) (lease corelock.Lease, free chan struct{}, until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clk.Now()
	current, busy := l.held[name]
	//: a live holding blocks us; hand back what to wait on.
	if busy && now.Before(current.deadline) {
		//: not ours — the caller waits on the holder's end or its deadline.
		return nil, current.free, current.deadline
	}
	//: an expired holding is taken over. Waking its waiters here rather than
	//: leaving the channel open is what stops a goroutine parked on a lease
	//: nobody will ever release.
	if busy {
		//: the previous holding is over, whether or not its holder knows.
		current.end()
	}
	//: the fence advances on EVERY acquisition, takeover included — that is
	//: the ordering the protected resource compares against.
	l.fences[name]++
	l.nextToken++
	next := &holding{
		token:    l.nextToken,
		fence:    l.fences[name],
		deadline: now.Add(l.ttl),
		free:     make(chan struct{}),
	}
	l.held[name] = next
	//: hand the caller a lease bound to this acquisition's token.
	return newMemoryLease(l, name, next), nil, time.Time{}
}

// await blocks until the current holding ends, its deadline passes, or ctx
// does. It returns a non-nil error only for ctx.
func (l *memoryLocker) await(ctx context.Context, free chan struct{}, until time.Time) error {
	//: the wait is armed against the holder's deadline, so expiry wakes us
	//: exactly then. A ManualClock makes this deterministic, which is why no
	//: test in this package sleeps.
	timer := l.clk.NewTimer(until.Sub(l.clk.Now()))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		//: the caller's patience ran out; that is the caller's error.
		return ctx.Err()
	case <-free:
		//: the holding ended — re-contend.
		return nil
	case <-timer.C():
		//: the deadline passed — re-contend.
		return nil
	}
}

// release ends the holding identified by token, if it is still the live one.
func (l *memoryLocker) release(name string, token uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, busy := l.held[name]
	//: someone else's holding, or none at all: release NOTHING. Unlocking a
	//: section another holder is inside is worse than leaking a lock.
	if !busy || current.token != token {
		//: the caller is told it no longer holds what it thinks it holds.
		return notHeld("Release", name, token)
	}
	//: ours — end it and wake every waiter.
	delete(l.held, name)
	current.end()
	//: an expired holding is still ours to clean up, but the caller ran past
	//: its lease and must be told so rather than reassured.
	if !l.clk.Now().Before(current.deadline) {
		//: the lock was reclaimable by anyone from the deadline onward.
		return notHeld("Release", name, token)
	}
	//: released while genuinely held.
	return nil
}

// extend renews the holding identified by token.
func (l *memoryLocker) extend(name string, token uint64) (deadline time.Time, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	current, busy := l.held[name]
	//: not ours any more — the caller is inside a section someone else owns.
	if !busy || current.token != token {
		//: LOCK_NOT_HELD, and the caller must stop.
		return time.Time{}, notHeld("Extend", name, token)
	}
	now := l.clk.Now()
	//: expiry is the DEADLINE, not "until someone else takes it". Renewing a
	//: lapsed lease would report success for a window in which any other
	//: caller was entitled to take the lock — which is precisely the moment a
	//: holder most needs to be told the truth.
	if !now.Before(current.deadline) {
		//: lapsed: refuse rather than quietly resurrect.
		return time.Time{}, notHeld("Extend", name, token)
	}
	//: a full lifetime from now, not from the old deadline: renewal is about
	//: the work still ahead, and stacking from the past would shrink every
	//: renewal after a slow one.
	current.deadline = now.Add(l.ttl)
	//: the new deadline, for the lease's own Deadline accessor.
	return current.deadline, nil
}

// notHeld builds the LOCK_NOT_HELD outcome with the operation that produced it.
func notHeld(op, name string, token uint64) error {
	//: restate the core sentinel's fields; the code is never re-Defined here.
	return kerrs.Wrap(corelock.LockNotHeld, kerrs.WrapParams{},
		kerrs.String("operation", op),
		kerrs.String("lock", name),
		kerrs.Int64("holder", int64(token)))
}

// checkName refuses a name no locker can use.
func checkName(name string) error {
	//: the empty string is what a forgotten assignment leaves behind; taking
	//: it as a legitimate key would funnel every such caller through one lock.
	if name == "" {
		//: LOCK_NAME_REJECTED.
		return kerrs.Wrap(corelock.LockNameRejected, kerrs.WrapParams{},
			kerrs.String("reason", "empty"))
	}
	//: usable.
	return nil
}
