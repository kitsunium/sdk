// Package lock — the background renewal, and the only channel through which a
// lost lease can reach work that has already started.
package lock

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
)

// KeepaliveConfig parameterises [Keepalive].
//
// Its zero value is deliberately NOT a working keepalive: Every must be
// stated, because a renewal cadence the SDK invented would be right only by
// accident against a lease lifetime it cannot see.
type KeepaliveConfig struct {
	// Every is the renewal period. It MUST be positive, and it MUST be
	// comfortably shorter than the lease TTL — a renewal that lands at the
	// deadline has already lost every race it could lose. A third of the TTL
	// leaves room for two consecutive failures.
	//
	// It is refused at zero rather than derived from the lease, because a
	// Lease deliberately does not expose its TTL: the port's job is to say
	// whether it can expire at all ([corelock.Deadliner]), not to let a helper
	// reconstruct a schedule the caller never chose.
	Every time.Duration
	// Clock is the time source the renewal ticks on. nil means clock.System.
	//
	// A caller testing expiry MUST pass the SAME clock the locker was built
	// with, or the renewals and the deadline will run on two different
	// timelines.
	Clock clock.Timed
}

// validate applies ADR 0031 to the renewal period.
func (c KeepaliveConfig) validate() error {
	//: a non-positive period cannot be a cadence. clock.NewTicker panics on
	//: one, and this refusal is what turns that panic into a typed error at
	//: the call site that can still do something about it.
	if c.Every <= 0 {
		//: refuse, naming the field.
		return kerrs.Wrap(corelock.LockMisconfigured, kerrs.WrapParams{},
			kerrs.String("option", "Every"),
			kerrs.String("value", c.Every.String()))
	}
	//: usable as given.
	return nil
}

// clockOrSystem resolves the configured time source.
func (c KeepaliveConfig) clockOrSystem() clock.Timed {
	//: a nil clock is the production default, not a misconfiguration.
	if c.Clock == nil {
		//: the system time source.
		return clock.System
	}
	//: the injected source, as given.
	return c.Clock
}

// Keepalive renews lease in the background and returns a context that is
// CANCELLED the instant the lease stops being held.
//
// # Why the loss arrives as a cancellation
//
// [corelock.Lease.Extend] returning an error is only useful to a caller that
// is currently calling it. The caller that needs the news is the one already
// running inside the critical section — and the only channel that reaches
// work in progress is its context. So a failed renewal cancels the derived
// context with [LockKeepaliveLost] as its cause, and the protected operation
// finds out through the mechanism it is already selecting on.
//
// This is the opposite of the drain signal in ADR 0043, and deliberately so.
// A drain is an invitation to finish, so cancelling would be wrong. Losing a
// lock is not an invitation: continuing means writing into a section another
// holder believes it owns, and the correct response is to stop.
//
//	ctx, stop, err := lock.Keepalive(ctx, lease, lock.KeepaliveConfig{Every: ttl / 3})
//	if err != nil { return err }
//	defer stop()
//	// ctx is now cancelled if the lease is lost; context.Cause(ctx) says so.
//
// The returned stop function releases the renewal goroutine and cancels the
// derived context. It does NOT release the lease — that stays the caller's
// job, because a helper that released on the way out would make the lifetime
// of the lock depend on the lifetime of a convenience.
//
// Goroutine lifecycle: exactly one goroutine is started, and it ends when the
// derived context ends — which happens on stop(), on the parent context
// ending, or on the first failed renewal. There is no path on which it
// outlives the returned stop function, so a caller that defers stop() leaks
// nothing.
func Keepalive(ctx context.Context, lease corelock.Lease, cfg KeepaliveConfig) (guarded context.Context, stop context.CancelFunc, err error) {
	//: the period is checked before a goroutine exists to run it.
	if invalid := cfg.validate(); invalid != nil {
		//: LockMisconfigured, naming the field.
		return nil, nil, invalid
	}
	//: a nil lease has nothing to renew, and a keepalive over it would report
	//: healthy forever — the exact shape of a lock that lies.
	if lease == nil {
		//: refuse rather than run.
		return nil, nil, kerrs.Wrap(corelock.LockNotHeld, kerrs.WrapParams{},
			kerrs.String("operation", "Keepalive"),
			kerrs.String("lock", ""))
	}
	derived, cancel := context.WithCancelCause(ctx)
	ticker := cfg.clockOrSystem().NewTicker(cfg.Every)
	go renewUntilLost(derived, cancel, ticker, lease)
	//: the caller's stop: it ends the goroutine and the derived context, and
	//: it never touches the lease.
	stop = func() {
		//: a deliberate stop is not a lost lease, so the cause is the plain
		//: cancellation — context.Cause stays informative.
		cancel(context.Canceled)
	}
	//: the context to hand to the protected work.
	return derived, stop, nil
}

// renewUntilLost extends lease on every tick until the derived context ends or
// a renewal fails.
//
// Goroutine lifecycle: this IS the keepalive's single goroutine. It returns on
// ctx.Done (stop, parent cancellation, or its own cancel below) and on the
// first failed renewal, and it stops the ticker on the way out — so it can
// neither outlive the context it was given nor leave a timer armed.
func renewUntilLost(ctx context.Context, cancel context.CancelCauseFunc, ticker clock.Ticker, lease corelock.Lease) {
	defer ticker.Stop()
	//: one renewal per tick, and one failure ends everything.
	for {
		select {
		case <-ctx.Done():
			//: stopped by the caller, or already lost. Either way there is
			//: nothing left to renew.
			return
		case _, open := <-ticker.C():
			//: a closed tick channel would otherwise spin this loop at full
			//: speed; an implementation that closes it means "no more ticks".
			if !open {
				//: nothing will ever fire again.
				return
			}
			//: the renewal runs on the derived context so a stop that lands
			//: mid-Extend is observed by the Extend itself.
			if err := lease.Extend(ctx); err != nil {
				//: the caller is inside a section it no longer owns. Cancel
				//: with a cause that names why, then stop renewing.
				cancel(keepaliveLost(err, lease.Fence()))
				//: nothing further to do — the caller reads context.Cause.
				return
			}
		}
	}
}

// keepaliveLost builds the cancellation cause for a failed renewal.
func keepaliveLost(cause error, fence uint64) error {
	//: restate the sentinel's fields; the code is never re-Defined here. When
	//: cause is already an *errs.Error — LOCK_NOT_HELD, normally — origin wins
	//: and this code joins the wrap trail, so both are recoverable.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:    CodeLockKeepaliveLost,
		Reason:  "LOCK_KEEPALIVE_LOST",
		Public:  "The lease could not be renewed and is no longer held",
		Private: "service/lock: a background Extend failed, so the derived context was cancelled with this cause; the fields name the fencing token that was lost",
	}, kerrs.Int64("fence", int64(fence)))
}
