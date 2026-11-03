package cache_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svclock "github.com/kitsunium/sdk/internal/service/app/lock"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// leasePoll is the step every in-process lease test waits on its manual
// clock.
const leasePoll time.Duration = 10 * time.Millisecond

// leaseOutcome is what a LoadLeased running on another goroutine answered.
type leaseOutcome struct {
	value string
	err   error
}

// leaserOf asserts the Leaser sibling on store.
func leaserOf(t *testing.T, store corecache.Store[string]) corecache.Leaser[string] {
	t.Helper()
	leaser, ok := store.(corecache.Leaser[string])
	if !ok {
		t.Fatalf("%T does not implement corecache.Leaser", store)
	}
	return leaser
}

// memoryLocker is the lock domain's in-process locker on manual, its leases
// long enough that only a test that means to let one lapse sees it lapse.
func memoryLocker(t *testing.T, manual *clock.ManualClock, ttl time.Duration) corelock.Locker {
	t.Helper()
	locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: ttl, Clock: manual})
	if err != nil {
		t.Fatalf("lock.NewMemory() = %v, want nil", err)
	}
	return locker
}

// loadLeasedAsync runs LoadLeased on its own goroutine and hands back its
// outcome.
func loadLeasedAsync(ctx context.Context, leaser corecache.Leaser[string], key string, fill corecache.Fill[string], locker corelock.Locker) <-chan leaseOutcome {
	done := make(chan leaseOutcome, 1)
	go func() {
		value, err := leaser.LoadLeased(ctx, key, fill, locker)
		done <- leaseOutcome{value: value, err: err}
	}()
	return done
}

// TestEveryStoreImplementsLeaser pins #830's sibling on both stores.
func TestEveryStoreImplementsLeaser(t *testing.T) {
	t.Parallel()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{}, near, far)
	if err != nil {
		t.Fatal(err)
	}
	leaserOf(t, near)
	leaserOf(t, chain)
}

// TestLoadLeasedWithoutALockerIsLoad pins the one reading of a nil locker.
func TestLoadLeasedWithoutALockerIsLoad(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	var fills atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		return corecache.EntryValue[string]{Value: "v"}, nil
	}
	for range 3 {
		value, err := leaserOf(t, store).LoadLeased(t.Context(), "k", fill, nil)
		if err != nil || value != "v" {
			t.Fatalf("LoadLeased(nil locker) = %q, %v; want v, nil", value, err)
		}
	}
	if got := fills.Load(); got != 1 {
		t.Fatalf("fill ran %d times, want 1", got)
	}
}

// TestANegativeLeasePollIsRefused applies ADR 0031 to the new field, on both
// constructors.
func TestANegativeLeasePollIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := svccache.NewMemory[string](svccache.MemoryConfig{MaxEntries: 8, LeasePoll: -time.Second}); !errs.HasCode(err, corecache.CodeCacheMisconfigured) {
		t.Fatalf("NewMemory(LeasePoll -1s) = %v, want CACHE_MISCONFIGURED", err)
	}
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	far := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	if _, err := svccache.NewChain(svccache.ChainConfig{LeasePoll: -time.Second}, near, far); !errs.HasCode(err, corecache.CodeCacheMisconfigured) {
		t.Fatalf("NewChain(LeasePoll -1s) = %v, want CACHE_MISCONFIGURED", err)
	}
}

// TestTheHolderAnswersTheWaiterThroughASharedTier is #830's central claim in
// one process: two chains standing for two processes — each its own near
// tier, both one far tier, both one locker. The first fills while the second
// waits without filling, and the second is answered by the first's value
// within one poll step of its clock: the origin sees ONE fill.
func TestTheHolderAnswersTheWaiterThroughASharedTier(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	locker := memoryLocker(t, manual, time.Hour)
	shared := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chainOf := func() corecache.Leaser[string] {
		chain, err := svccache.NewChain(svccache.ChainConfig{Clock: manual, LeasePoll: leasePoll},
			newStore(t, svccache.MemoryConfig{MaxEntries: 8}), shared)
		if err != nil {
			t.Fatal(err)
		}
		return leaserOf(t, chain)
	}
	holder, waiter := chainOf(), chainOf()
	var fills atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	holderFill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		close(started)
		<-release
		return corecache.EntryValue[string]{Value: "from-holder", TTL: corecache.NoExpiry}, nil
	}
	waiterFill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		return corecache.EntryValue[string]{Value: "from-waiter", TTL: corecache.NoExpiry}, nil
	}
	held := loadLeasedAsync(t.Context(), holder, "hot", holderFill, locker)
	<-started
	waited := loadLeasedAsync(t.Context(), waiter, "hot", waiterFill, locker)
	//: the waiter found the lease held and armed its step; a few steps while
	//: the holder still fills change nothing.
	for range 3 {
		manual.BlockUntil(1)
		manual.Advance(leasePoll)
	}
	manual.BlockUntil(1)
	close(release)
	if outcome := <-held; outcome.err != nil || outcome.value != "from-holder" {
		t.Fatalf("holder LoadLeased = %q, %v; want from-holder, nil", outcome.value, outcome.err)
	}
	//: one step after the holder stored, the waiter reads its value.
	manual.Advance(leasePoll)
	if outcome := <-waited; outcome.err != nil || outcome.value != "from-holder" {
		t.Fatalf("waiter LoadLeased = %q, %v; want the holder's from-holder, nil", outcome.value, outcome.err)
	}
	if got := fills.Load(); got != 1 {
		t.Fatalf("the origin was filled %d times across the two processes, want 1", got)
	}
}

// TestAMemoryStoreAloneTakesTurns states the limit #830 keeps honest: two
// memory stores share no value, so the lease cannot spare the second fill —
// it only keeps the two from running at once.
func TestAMemoryStoreAloneTakesTurns(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	locker := memoryLocker(t, manual, time.Hour)
	first := leaserOf(t, newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual, LeasePoll: leasePoll}))
	second := leaserOf(t, newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual, LeasePoll: leasePoll}))
	var running, overlapped, fills atomic.Int64
	started, release := make(chan struct{}), make(chan struct{})
	fillWith := func(value string, gate bool) corecache.Fill[string] {
		return func(context.Context) (corecache.EntryValue[string], error) {
			fills.Add(1)
			if running.Add(1) > 1 {
				overlapped.Add(1)
			}
			defer running.Add(-1)
			if gate {
				close(started)
				<-release
			}
			return corecache.EntryValue[string]{Value: value, TTL: corecache.NoExpiry}, nil
		}
	}
	firstDone := loadLeasedAsync(t.Context(), first, "k", fillWith("one", true), locker)
	<-started
	secondDone := loadLeasedAsync(t.Context(), second, "k", fillWith("two", false), locker)
	for range 3 {
		manual.BlockUntil(1)
		manual.Advance(leasePoll)
	}
	manual.BlockUntil(1)
	if got := fills.Load(); got != 1 {
		t.Fatalf("%d fills while the first held the lease, want 1", got)
	}
	close(release)
	if outcome := <-firstDone; outcome.err != nil || outcome.value != "one" {
		t.Fatalf("first LoadLeased = %q, %v; want one, nil", outcome.value, outcome.err)
	}
	manual.Advance(leasePoll)
	if outcome := <-secondDone; outcome.err != nil || outcome.value != "two" {
		t.Fatalf("second LoadLeased = %q, %v; want its own two, nil", outcome.value, outcome.err)
	}
	if fills.Load() != 2 || overlapped.Load() != 0 {
		t.Fatalf("fills = %d, overlapping = %d; want 2 taking turns, 0 overlapping", fills.Load(), overlapped.Load())
	}
}

// TestALapsedLeaseOfADeadHolderLetsTheWaiterFill is the Deadliner half of
// #830: a holder that took the lease and never came back — no Set, no
// Release — holds the key only until its lease lapses, and the waiter then
// fills once.
func TestALapsedLeaseOfADeadHolderLetsTheWaiterFill(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	const ttl = time.Second
	locker := memoryLocker(t, manual, ttl)
	if _, held, err := locker.TryAcquire(t.Context(), "cache:k"); !held || err != nil {
		t.Fatalf("TryAcquire = %v, %v; want held", held, err)
	}
	store := leaserOf(t, newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual, LeasePoll: leasePoll}))
	var fills atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		return corecache.EntryValue[string]{Value: "after"}, nil
	}
	done := loadLeasedAsync(t.Context(), store, "k", fill, locker)
	for elapsed := time.Duration(0); elapsed < ttl-leasePoll; elapsed += leasePoll {
		manual.BlockUntil(1)
		manual.Advance(leasePoll)
	}
	manual.BlockUntil(1)
	if got := fills.Load(); got != 0 {
		t.Fatalf("the waiter filled %d times while the dead holder's lease was live, want 0", got)
	}
	manual.Advance(2 * leasePoll)
	if outcome := <-done; outcome.err != nil || outcome.value != "after" {
		t.Fatalf("LoadLeased = %q, %v; want after, nil", outcome.value, outcome.err)
	}
	if got := fills.Load(); got != 1 {
		t.Fatalf("fill ran %d times, want 1", got)
	}
}

// TestAWaiterLeavesWithItsOwnContext pins that a waiting process never fills
// and never outlives its caller's context.
func TestAWaiterLeavesWithItsOwnContext(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	locker := memoryLocker(t, manual, time.Hour)
	if _, held, err := locker.TryAcquire(t.Context(), "cache:k"); !held || err != nil {
		t.Fatalf("TryAcquire = %v, %v; want held", held, err)
	}
	store := leaserOf(t, newStore(t, svccache.MemoryConfig{MaxEntries: 8, Clock: manual, LeasePoll: leasePoll}))
	ctx, cancel := context.WithCancel(t.Context())
	var fills atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		return corecache.EntryValue[string]{Value: "never"}, nil
	}
	done := loadLeasedAsync(ctx, store, "k", fill, locker)
	manual.BlockUntil(1)
	cancel()
	if outcome := <-done; !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("LoadLeased after cancel = %q, %v; want context.Canceled", outcome.value, outcome.err)
	}
	if got := fills.Load(); got != 0 {
		t.Fatalf("a waiter filled %d times, want 0", got)
	}
}

// brokenLocker is a locker whose backend cannot answer.
type brokenLocker struct{ cause error }

// Acquire reports the backend's failure.
func (b brokenLocker) Acquire(context.Context, string) (corelock.Lease, error) {
	return nil, b.cause
}

// TryAcquire reports the backend's failure.
func (b brokenLocker) TryAcquire(context.Context, string) (corelock.Lease, bool, error) {
	return nil, false, b.cause
}

// TestALockerThatCannotAnswerIsLeaseFailed pins CACHE_LEASE_FAILED, the
// locker's own error kept on the trail, and no fill.
func TestALockerThatCannotAnswerIsLeaseFailed(t *testing.T) {
	t.Parallel()
	cause := errors.New("backend down")
	store := leaserOf(t, newStore(t, svccache.MemoryConfig{MaxEntries: 8}))
	var fills atomic.Int64
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		fills.Add(1)
		return corecache.EntryValue[string]{Value: "v"}, nil
	}
	_, err := store.LoadLeased(t.Context(), "k", fill, brokenLocker{cause: cause})
	if !errs.HasCode(err, corecache.CodeCacheLeaseFailed) || !errors.Is(err, cause) {
		t.Fatalf("LoadLeased(broken locker) = %v; want CACHE_LEASE_FAILED carrying the cause", err)
	}
	if got := fills.Load(); got != 0 {
		t.Fatalf("fill ran %d times without a lease, want 0", got)
	}
}

// TestAHitNeverTouchesTheLocker pins the hit path: Fetch first, as Load does.
func TestAHitNeverTouchesTheLocker(t *testing.T) {
	t.Parallel()
	store := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	mustSet(t, store, "k", corecache.EntryValue[string]{Value: "cached"})
	value, err := leaserOf(t, store).LoadLeased(t.Context(), "k", nil, brokenLocker{cause: errors.New("unreachable")})
	if err != nil || value != "cached" {
		t.Fatalf("LoadLeased on a hit = %q, %v; want cached, nil", value, err)
	}
}
