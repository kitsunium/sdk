package lock_test

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// renewEvery is the keepalive period every test here uses: a third of the
// lease, which leaves room for two consecutive failed renewals.
const renewEvery time.Duration = testTTL / 3

func TestKeepaliveRefusesANonPositivePeriod(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	ctx, stop, err := svclock.Keepalive(t.Context(), lease, svclock.KeepaliveConfig{})
	if ctx != nil || stop != nil {
		t.Fatal("a keepalive was started with no renewal period")
	}
	//: clock.NewTicker would PANIC on this; refusing turns that into a typed
	//: error at a call site that can still do something about it.
	if !errs.HasCode(err, corelock.CodeLockMisconfigured) {
		t.Fatalf("Keepalive = %v, want LOCK_MISCONFIGURED", err)
	}
}

// TestKeepaliveRefusesANilLease pins the fail-open this helper would otherwise
// be: a keepalive over nothing renews nothing and reports healthy forever.
func TestKeepaliveRefusesANilLease(t *testing.T) {
	t.Parallel()
	ctx, stop, err := svclock.Keepalive(t.Context(), nil, svclock.KeepaliveConfig{Every: renewEvery})
	if ctx != nil || stop != nil {
		t.Fatal("a keepalive was started over a nil lease")
	}
	if !errs.HasCode(err, corelock.CodeLockNotHeld) {
		t.Fatalf("Keepalive = %v, want LOCK_NOT_HELD", err)
	}
}

// TestKeepaliveHoldsTheLeasePastItsDeadline is the renewal decision working
// end to end: the clock runs well past the original TTL and the lock is still
// held, because the background renewal kept moving the deadline.
func TestKeepaliveHoldsTheLeasePastItsDeadline(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	guarded, stop, err := svclock.Keepalive(t.Context(), lease, svclock.KeepaliveConfig{
		Every: renewEvery,
		Clock: manual,
	})
	if err != nil {
		t.Fatalf("Keepalive = %v", err)
	}
	defer stop()
	//: five renewal periods — one and two thirds of the whole lease.
	for range 5 {
		manual.BlockUntil(1)
		manual.Advance(renewEvery)
		waitForDeadline(t, lease, manual.Now().Add(testTTL))
	}
	if guarded.Err() != nil {
		t.Fatalf("the guarded context was cancelled: %v", context.Cause(guarded))
	}
	_, held, tryErr := locker.TryAcquire(t.Context(), "job")
	if tryErr != nil {
		t.Fatalf("TryAcquire = %v", tryErr)
	}
	if held {
		t.Fatal("the lock was taken over despite a running keepalive")
	}
}

// TestKeepaliveCancelsTheContextWhenTheLeaseIsLost is the reason this helper
// exists at all.
//
// Extend returning an error only helps a caller that is currently calling it.
// The caller that NEEDS the news is the one already inside the section — and
// the only channel that reaches work in progress is its context. So the loss
// arrives as a cancellation whose cause names it.
func TestKeepaliveCancelsTheContextWhenTheLeaseIsLost(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	guarded, stop, err := svclock.Keepalive(t.Context(), lease, svclock.KeepaliveConfig{
		Every: renewEvery,
		Clock: manual,
	})
	if err != nil {
		t.Fatalf("Keepalive = %v", err)
	}
	defer stop()
	//: the keepalive's ticker is armed; now jump PAST the deadline in one
	//: step, so the next renewal finds a lapsed lease.
	manual.BlockUntil(1)
	manual.Advance(testTTL + renewEvery)
	<-guarded.Done()
	cause := context.Cause(guarded)
	//: origin wins (ADR 0005): the cause IS the lost lock, and the keepalive's
	//: own code rides the wrap trail so both are recoverable.
	if !errs.HasCode(cause, corelock.CodeLockNotHeld) {
		t.Fatalf("context.Cause = %v, want LOCK_NOT_HELD", cause)
	}
}

// TestStoppingAKeepaliveIsNotALostLease pins the distinction that makes
// context.Cause worth reading: a deliberate stop is a plain cancellation, not
// an alarm.
func TestStoppingAKeepaliveIsNotALostLease(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	guarded, stop, err := svclock.Keepalive(t.Context(), lease, svclock.KeepaliveConfig{
		Every: renewEvery,
		Clock: manual,
	})
	if err != nil {
		t.Fatalf("Keepalive = %v", err)
	}
	stop()
	<-guarded.Done()
	if cause := context.Cause(guarded); !errors.Is(cause, context.Canceled) {
		t.Fatalf("context.Cause after stop = %v, want context.Canceled", cause)
	}
	//: and stopping the keepalive must NOT have released the lease: the
	//: lifetime of a lock does not depend on the lifetime of a convenience.
	_, held, tryErr := locker.TryAcquire(t.Context(), "job")
	if tryErr != nil {
		t.Fatalf("TryAcquire = %v", tryErr)
	}
	if held {
		t.Fatal("stopping the keepalive released the lease")
	}
}

// waitForDeadline blocks until the lease's deadline has reached want, so a
// test never races the renewal goroutine. It uses the lease's own Deadliner
// sibling, which is the only observable the port offers — and it spins on the
// MANUAL clock's state rather than on wall time, so nothing here sleeps.
func waitForDeadline(t *testing.T, lease corelock.Lease, want time.Time) {
	t.Helper()
	deadliner, ok := lease.(corelock.Deadliner)
	if !ok {
		t.Fatal("a memory lease does not implement Deadliner")
	}
	//: the renewal happens on another goroutine; yield until it lands. The
	//: test binary's own timeout bounds this, as ManualClock.BlockUntil does.
	for deadliner.Deadline().Before(want) {
		runtimeYield()
	}
}

// runtimeYield hands the processor to another goroutine without introducing a
// wall-clock wait. It is the ONLY waiting primitive this package's tests use;
// see TestPackageNeverWaitsOnTheWallClock.
func runtimeYield() {
	//: Gosched, never Sleep: a sleep would make this suite's timing meaningful,
	//: and a suite whose timing is meaningful is a suite that flakes.
	runtime.Gosched()
}
