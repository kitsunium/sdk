package cache

import (
	"context"
	"errors"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultLeasePoll is the step a process waiting on another's lease reads the
// store again at when the configuration says nothing: the file locker's own
// poll, so waiting for a lease and waiting for its value keep one cadence.
const defaultLeasePoll time.Duration = 25 * time.Millisecond

// leasePrefix opens every lock name a fill is leased under, so a cache's
// leases never share a name with another user of the same locker.
const leasePrefix string = "cache:"

// leased is one LoadLeased miss, after the in-process collapse: what to read,
// how to fill, and how to wait between two reads (#830).
type leased[V any] struct {
	locker corelock.Locker
	waiter clock.Waiter
	poll   time.Duration
	// lookup reads the store: found is a hit, err a hit's own answer (a
	// negative entry) or a read that failed.
	lookup func(ctx context.Context) (value V, found bool, err error)
	// fill runs the caller's fill and stores what it answered.
	fill func(ctx context.Context) (value V, err error)
}

// run takes the key's lease, or waits for whoever holds it.
//
// A process that finds the lease held fills nothing: it waits one step, reads
// the store again — the holder's value may have landed in a tier both share —
// and tries the lease once more, which succeeds only once the holder released
// it, died, or let an expiring lease lapse. Whoever takes the lease reads
// again before filling, so a value stored just before the release is never
// filled twice.
func (l leased[V]) run(ctx context.Context, key string) (value V, err error) {
	name := leasePrefix + key
	//: until a value, an answer, the lease, or the end of ctx.
	for {
		lease, held, tryErr := l.locker.TryAcquire(ctx, name)
		//: a locker that cannot answer protects nothing; say so.
		if tryErr != nil {
			var zero V
			//: CACHE_LEASE_FAILED, the locker's error kept.
			return zero, leaseFailure("acquire", key, tryErr)
		}
		//: this process fills, under the lease.
		if held {
			//: read, fill, store, release.
			return l.holding(ctx, key, lease)
		}
		//: another process holds it: one step, then read again.
		if waitErr := l.wait(ctx); waitErr != nil {
			var zero V
			//: this caller's own end.
			return zero, waitErr
		}
		//: the holder's value, or a hit's answer, ends the wait.
		if found, hit, lookErr := l.read(ctx); found || lookErr != nil {
			//: served without a fill of this process's.
			return hit, lookErr
		}
	}
}

// read is lookup with its results ordered for run's test.
func (l leased[V]) read(ctx context.Context) (found bool, value V, err error) {
	value, found, err = l.lookup(ctx)
	//: as lookup answered.
	return found, value, err
}

// holding runs under the lease: one more read, then the fill. The lease is
// released whatever happens — a failed fill, a panic, a ctx already ended —
// on a context that cannot be cancelled, since a lease left behind keeps
// every other process waiting until it dies.
func (l leased[V]) holding(ctx context.Context, key string, lease corelock.Lease) (value V, err error) {
	defer func() {
		releaseErr := lease.Release(context.WithoutCancel(ctx))
		//: the call's own failure says more than the release's.
		if releaseErr != nil && err == nil {
			var zero V
			value, err = zero, leaseFailure("release", key, releaseErr)
		}
	}()
	//: the previous holder may have stored the value just before releasing.
	if found, hit, lookErr := l.read(ctx); found || lookErr != nil {
		//: nothing to fill.
		return hit, lookErr
	}
	//: the one fill.
	return l.fill(ctx)
}

// wait blocks one poll step on the waiter, or until ctx ends.
func (l leased[V]) wait(ctx context.Context) error {
	timer := l.waiter.NewTimer(l.poll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		//: the caller left; nothing was filled.
		return ctx.Err()
	case <-timer.C():
		//: the step passed.
		return nil
	}
}

// waiterOf returns what clk waits with: itself when it can wait, the system
// clock when it only reads time (a clock.CoarseClock).
func waiterOf(clk clock.Clock) clock.Waiter {
	//: clock.System and a clock.ManualClock can wait.
	if waiter, ok := clk.(clock.Waiter); ok {
		//: the configured clock.
		return waiter
	}
	//: a reading-only clock leaves the wait to the system clock.
	return clock.System
}

// pollOrDefault resolves a configured lease poll: zero is defaultLeasePoll.
func pollOrDefault(poll time.Duration) time.Duration {
	//: the zero is the default, not "never wait".
	if poll == 0 {
		//: the file locker's own step.
		return defaultLeasePoll
	}
	//: as configured; a negative one was refused at construction.
	return poll
}

// LoadLeased is Load with a cross-process lease (#830). See
// [corecache.Leaser]: on a memory store alone, another process never sees
// this one's value, so the lease only takes turns.
func (s *memoryStore[V]) LoadLeased(ctx context.Context, key string, fill corecache.Fill[V], locker corelock.Locker) (value V, err error) {
	//: no locker, no lease: the one reading is Load.
	if locker == nil {
		//: in-process collapse only.
		return s.Load(ctx, key, fill)
	}
	//: the hit path takes neither the group nor the lease.
	if rec, ok := s.fetchRecord(key); ok {
		//: a negative entry is an answer too.
		return rec.value, rec.absent
	}
	filled, _, groupErr := s.group.Do(ctx, key, func(callCtx context.Context) (V, error) {
		//: one goroutine per key in this process; then the lease.
		return leased[V]{
			locker: locker,
			waiter: s.waiter,
			poll:   s.leasePoll,
			lookup: func(context.Context) (V, bool, error) {
				rec, ok := s.fetchRecord(key)
				//: a negative entry is found, its error the answer.
				return rec.value, ok, rec.absent
			},
			fill: func(fillCtx context.Context) (V, error) {
				//: stored as Load stores, a negative answer included.
				return s.fillAndStore(fillCtx, key, fill)
			},
		}.run(callCtx, key)
	})
	//: the shared outcome, or this caller's own cancellation.
	return filled, groupErr
}

// LoadLeased is Load with a cross-process lease (#830): a process waiting on
// another's lease reads the chain again every step, so a tier both reach
// answers it with the holder's value. See [corecache.Leaser].
func (c *chainStore[V]) LoadLeased(ctx context.Context, key string, fill corecache.Fill[V], locker corelock.Locker) (value V, err error) {
	//: no locker, no lease: the one reading is Load.
	if locker == nil {
		//: in-process collapse only.
		return c.Load(ctx, key, fill)
	}
	//: the hit path takes neither the group nor the lease.
	if hit, ok, fetchErr := c.Fetch(ctx, key); fetchErr == nil && ok {
		//: served from a tier.
		return hit, nil
	}
	filled, _, groupErr := c.group.Do(ctx, key, func(callCtx context.Context) (V, error) {
		//: one goroutine per key in this process; then the lease.
		return leased[V]{
			locker: locker,
			waiter: c.waiter,
			poll:   c.leasePoll,
			lookup: func(lookCtx context.Context) (V, bool, error) {
				hit, ok, fetchErr := c.Fetch(lookCtx, key)
				//: a failing tier reads as a miss, as Load reads it.
				return hit, ok && fetchErr == nil, nil
			},
			fill: func(fillCtx context.Context) (V, error) {
				//: write-through, as Load's fill.
				return c.fillAndSet(fillCtx, key, fill)
			},
		}.run(callCtx, key)
	})
	//: the shared outcome, or this caller's own cancellation.
	return filled, groupErr
}

// leaseFailure labels a locker's error with the operation and the key, the
// rule tierFailure follows: an untyped cause stays on the chain so errors.Is
// still finds it; a typed one, which wrapping would let take over the code,
// travels as fields.
func leaseFailure(operation, key string, cause error) error {
	fields := []kerrs.FieldValue{
		kerrs.String("operation", operation),
		kerrs.String("key", key),
		kerrs.String("cause", cause.Error()),
	}
	//: a typed cause would take over the code if wrapped; label it instead.
	if typedErr, typed := errors.AsType[*kerrs.Error](cause); typed {
		//: CACHE_LEASE_FAILED, the cause's own code in a field.
		return kerrs.Wrap(corecache.CacheLeaseFailed, kerrs.WrapParams{},
			append(fields, kerrs.String("cause_code", typedErr.Code().String()))...)
	}
	//: an untyped cause, IN the chain, under CACHE_LEASE_FAILED.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:    corecache.CacheLeaseFailed.Code(),
		Reason:  corecache.CacheLeaseFailed.Reason(),
		Public:  corecache.CacheLeaseFailed.Public(),
		Private: corecache.CacheLeaseFailed.Private(),
	}, fields...)
}
