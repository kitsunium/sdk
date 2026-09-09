package lock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// testTTL is the lease lifetime every memory test is built on. Its value is
// irrelevant: nothing here waits for it, a ManualClock jumps to it.
const testTTL time.Duration = time.Minute

// newTestLocker builds a memory locker on a manual clock and returns both.
func newTestLocker(t *testing.T) (corelock.Locker, *clock.ManualClock) {
	t.Helper()
	manual := clock.NewManualClock(time.Unix(0, 0).UTC())
	locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: testTTL, Clock: manual})
	if err != nil {
		t.Fatalf("NewMemory = %v, want a locker", err)
	}
	return locker, manual
}

// TestNewMemoryRefusesANonPositiveTTL is ADR 0031's refuse half, and the
// single most important test in this package.
//
// A zero TTL has two natural readings and they are opposites: "expires
// immediately" and "never expires". A locker built on the first grants every
// Acquire and excludes nobody, while reporting success on every call — which
// is indistinguishable, from outside, from a lock nobody contends. There is no
// defensible default, so there is no default.
func TestNewMemoryRefusesANonPositiveTTL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ttl  time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: tc.ttl})
			if locker != nil {
				t.Fatal("a locker was built from a TTL that cannot be honoured")
			}
			if !errs.HasCode(err, corelock.CodeLockMisconfigured) {
				t.Fatalf("NewMemory = %v, want LOCK_MISCONFIGURED", err)
			}
		})
	}
}

func TestAnEmptyNameIsRefused(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	_, err := locker.Acquire(t.Context(), "")
	if !errs.HasCode(err, corelock.CodeLockNameRejected) {
		t.Fatalf("Acquire(\"\") = %v, want LOCK_NAME_REJECTED", err)
	}
}

func TestASecondCallerIsExcluded(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	first, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v, want a lease", err)
	}
	second, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error", err)
	}
	//: "held elsewhere" is not an error — the caller offered to give up.
	if held || second != nil {
		t.Fatal("a second caller acquired a lock that was already held")
	}
	if relErr := first.Release(t.Context()); relErr != nil {
		t.Fatalf("Release = %v, want nil", relErr)
	}
	_, held, err = locker.TryAcquire(t.Context(), "job")
	if err != nil || !held {
		t.Fatalf("TryAcquire after Release = (%t, %v), want (true, nil)", held, err)
	}
}

// TestALapsedLeaseIsTakenOverAndTheFenceAdvances drives the hazard the whole
// domain is shaped around, without sleeping: the clock jumps past the
// deadline, a second caller takes the lock, and the FIRST HOLDER IS STILL
// RUNNING and has been told nothing.
//
// The fence is what survives that: the old holder carries 1, the new one
// carries 2, and a resource comparing them rejects the stale write.
func TestALapsedLeaseIsTakenOverAndTheFenceAdvances(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	first, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v, want a lease", err)
	}
	if first.Fence() != 1 {
		t.Fatalf("first Fence = %d, want 1", first.Fence())
	}
	//: the deadline passes. Nothing interrupts the first holder — that is the
	//: entire point of the scenario.
	manual.Advance(testTTL)
	second, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil || !held {
		t.Fatalf("TryAcquire after expiry = (%t, %v), want (true, nil)", held, err)
	}
	if second.Fence() != 2 {
		t.Fatalf("second Fence = %d, want 2 — the fence must advance on takeover", second.Fence())
	}
}

// TestTheOldHolderCannotReleaseAfterTakeover is the ownership decision, made
// executable.
//
// The lapsed holder calls Release exactly as it always does. If Release
// unlocked by NAME it would end the section the new holder is inside — and the
// new holder would never learn of it. So Release checks the token, releases
// nothing, and says LOCK_NOT_HELD.
func TestTheOldHolderCannotReleaseAfterTakeover(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	stale, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v, want a lease", err)
	}
	manual.Advance(testTTL)
	current, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil || !held {
		t.Fatalf("TryAcquire after expiry = (%t, %v), want (true, nil)", held, err)
	}
	//: the stale holder tries to tidy up.
	if relErr := stale.Release(t.Context()); !errs.HasCode(relErr, corelock.CodeLockNotHeld) {
		t.Fatalf("stale Release = %v, want LOCK_NOT_HELD", relErr)
	}
	//: and the current holder must STILL hold it. This is the assertion that
	//: would fail if Release unlocked by name.
	_, stolen, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error", err)
	}
	if stolen {
		t.Fatal("the stale holder's Release unlocked the current holder's section")
	}
	if relErr := current.Release(t.Context()); relErr != nil {
		t.Fatalf("current Release = %v, want nil", relErr)
	}
}

// TestExtendKeepsTheLockPastItsOriginalDeadline is the renewal decision: a
// lease that is renewed before its deadline survives it, so a long job is not
// a bet on finishing in time.
func TestExtendKeepsTheLockPastItsOriginalDeadline(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v, want a lease", err)
	}
	//: most of the way to the deadline, then renew.
	manual.Advance(testTTL - time.Second)
	if extErr := lease.Extend(t.Context()); extErr != nil {
		t.Fatalf("Extend = %v, want nil", extErr)
	}
	//: past the ORIGINAL deadline, but not past the renewed one.
	manual.Advance(2 * time.Second)
	_, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("TryAcquire = %v, want no error", err)
	}
	if held {
		t.Fatal("a renewed lease was taken over past its ORIGINAL deadline")
	}
	deadliner, ok := lease.(corelock.Deadliner)
	if !ok {
		t.Fatal("a memory lease does not implement Deadliner — a caller cannot ask whether it can expire")
	}
	//: the renewal is a full lifetime from the moment of renewal, not from the
	//: old deadline: renewal is about the work still ahead.
	want := time.Unix(0, 0).UTC().Add(testTTL - time.Second).Add(testTTL)
	if !deadliner.Deadline().Equal(want) {
		t.Fatalf("Deadline = %v, want %v", deadliner.Deadline(), want)
	}
}

// TestExtendOnALapsedLeaseIsRefused pins the sharpest edge of the renewal
// decision: expiry is the DEADLINE, not "until somebody else takes it".
//
// Renewing a lapsed lease would report success for the whole window in which
// any other caller was entitled to take the lock — which is exactly the moment
// a holder most needs to be told the truth.
func TestExtendOnALapsedLeaseIsRefused(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v, want a lease", err)
	}
	//: past the deadline, and NOBODY has taken the lock.
	manual.Advance(testTTL)
	if extErr := lease.Extend(t.Context()); !errs.HasCode(extErr, corelock.CodeLockNotHeld) {
		t.Fatalf("Extend on a lapsed lease = %v, want LOCK_NOT_HELD", extErr)
	}
}

// TestTheFenceIsPerNameAndNeverZero pins the two properties a fencing token
// must have to be usable: it orders acquisitions OF ONE LOCK, and it never
// takes the value a caller who forgot to plumb it through would produce.
func TestTheFenceIsPerNameAndNeverZero(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	alpha, err := locker.Acquire(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("Acquire alpha = %v", err)
	}
	beta, err := locker.Acquire(t.Context(), "beta")
	if err != nil {
		t.Fatalf("Acquire beta = %v", err)
	}
	//: two DIFFERENT locks each start at 1 — a fence orders one name's
	//: acquisitions, not the locker's.
	if alpha.Fence() != 1 || beta.Fence() != 1 {
		t.Fatalf("fences = (%d, %d), want (1, 1)", alpha.Fence(), beta.Fence())
	}
	if relErr := alpha.Release(t.Context()); relErr != nil {
		t.Fatalf("Release = %v", relErr)
	}
	again, err := locker.Acquire(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("Acquire alpha again = %v", err)
	}
	//: a clean release-then-reacquire advances the fence too: the resource
	//: cannot tell a takeover from a handover, so both must order.
	if again.Fence() != 2 {
		t.Fatalf("re-acquired Fence = %d, want 2", again.Fence())
	}
}

func TestReleasingTwiceIsNotAnError(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("first Release = %v, want nil", relErr)
	}
	//: the caller wanted the lock gone and it is gone. Reporting an error here
	//: would make `defer lease.Release(ctx)` unsafe next to an explicit one.
	if relErr := lease.Release(t.Context()); relErr != nil {
		t.Fatalf("second Release = %v, want nil", relErr)
	}
}

// TestReleaseWorksOnACancelledContext pins a decision that is easy to get
// backwards. The usual call site is `defer lease.Release(ctx)` with the very
// context whose cancellation ended the work; honouring it there would mean
// every timed-out operation leaks its lease for a full TTL.
func TestReleaseWorksOnACancelledContext(t *testing.T) {
	t.Parallel()
	locker, _ := newTestLocker(t)
	lease, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if relErr := lease.Release(cancelled); relErr != nil {
		t.Fatalf("Release on a cancelled context = %v, want nil", relErr)
	}
	_, held, err := locker.TryAcquire(t.Context(), "job")
	if err != nil || !held {
		t.Fatalf("the lock was not released: TryAcquire = (%t, %v)", held, err)
	}
}

// TestAcquireWakesWhenTheHolderReleases proves a blocked Acquire is woken by a
// release rather than by a poll — the clock is never advanced here at all.
//
// Goroutine lifecycle: one waiter, which returns as soon as its Acquire
// resolves. The test receives on `acquired` before asserting, so the receive
// is what joins it and nothing is left running.
func TestAcquireWakesWhenTheHolderReleases(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	holder, err := locker.Acquire(t.Context(), "job")
	if err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	acquired := make(chan corelock.Lease, 1)
	go func() {
		lease, acqErr := locker.Acquire(t.Context(), "job")
		if acqErr != nil {
			close(acquired)
			return
		}
		acquired <- lease
	}()
	//: wait until the waiter has armed its deadline timer, so the release
	//: below cannot land before the waiter is actually waiting.
	manual.BlockUntil(1)
	if relErr := holder.Release(t.Context()); relErr != nil {
		t.Fatalf("Release = %v", relErr)
	}
	lease, ok := <-acquired
	if !ok {
		t.Fatal("the blocked Acquire failed instead of taking the released lock")
	}
	//: a handover advances the fence exactly as a takeover does.
	if lease.Fence() != 2 {
		t.Fatalf("Fence = %d, want 2", lease.Fence())
	}
}

// TestAcquireWakesWhenTheLeaseLapses is the same wake-up through the other
// door: nobody releases, the deadline simply passes.
//
// Goroutine lifecycle: one waiter, joined by the receive on `acquired`, as in
// the test above.
func TestAcquireWakesWhenTheLeaseLapses(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	if _, err := locker.Acquire(t.Context(), "job"); err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	acquired := make(chan corelock.Lease, 1)
	go func() {
		lease, acqErr := locker.Acquire(t.Context(), "job")
		if acqErr != nil {
			close(acquired)
			return
		}
		acquired <- lease
	}()
	manual.BlockUntil(1)
	//: the holder never releases. The lease lapses and the waiter takes over —
	//: without this package sleeping for a nanosecond.
	manual.Advance(testTTL)
	lease, ok := <-acquired
	if !ok {
		t.Fatal("the blocked Acquire failed instead of taking the lapsed lock")
	}
	if lease.Fence() != 2 {
		t.Fatalf("Fence = %d, want 2", lease.Fence())
	}
}

// TestAcquireHonoursItsContext pins that a blocked Acquire returns the
// CALLER's error rather than an SDK sentinel.
//
// Goroutine lifecycle: one waiter, joined by the receive on `failed`, which
// cannot happen until its Acquire has returned.
func TestAcquireHonoursItsContext(t *testing.T) {
	t.Parallel()
	locker, manual := newTestLocker(t)
	if _, err := locker.Acquire(t.Context(), "job"); err != nil {
		t.Fatalf("Acquire = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	failed := make(chan error, 1)
	go func() {
		_, acqErr := locker.Acquire(ctx, "job")
		failed <- acqErr
	}()
	manual.BlockUntil(1)
	cancel()
	//: the caller supplied the deadline and already knows what it means, so it
	//: gets its own error rather than an SDK sentinel.
	if err := <-failed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire = %v, want context.Canceled", err)
	}
}
