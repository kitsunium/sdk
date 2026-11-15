package singleflight

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waiters reports how many callers are attached to key's in-flight call. It is
// declared in the internal test file on purpose: it is the BARRIER the strict
// dedup assertion needs, and nothing outside a test has a legitimate use for
// it — a caller that branches on the waiter count is racing the number it just
// read.
func (g *Group[K, V]) waiters(key K) int {
	held := g.shardOf(key)
	held.mu.Lock()
	defer held.unlock()
	//: an absent key, or a retired call still in the map, has no waiters.
	entry, found := held.calls[key]
	if !found || entry.retired.Load() {
		//: no call in flight for this key.
		return 0
	}
	//: the count join maintains.
	return entry.refs
}

// awaitJoined blocks until n callers are attached to key, or fails the test.
// This is what makes the "exactly one execution" claim deterministic instead
// of a sleep long enough to usually work.
func awaitJoined[K comparable, V any](t *testing.T, g *Group[K, V], key K, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for g.waiters(key) != n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d callers joined key %v", g.waiters(key), n, key)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestConcurrentCallersRunFnExactlyOnce is the primitive's whole claim, and it
// is asserted against a real barrier: fn is released only once every caller is
// provably attached to the same in-flight call.
//
// Goroutine lifecycle: one per caller, started here and joined by wg.Wait
// before any assertion; each terminates when Do returns, which the closing of
// release guarantees.
func TestConcurrentCallersRunFnExactlyOnce(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	var runs atomic.Int64
	release := make(chan struct{})
	const callers int = 64

	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			val, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
				runs.Add(1)
				<-release
				return 7, nil
			})
			if err != nil || val != 7 {
				t.Errorf("Do = (%d, %v), want (7, nil)", val, err)
			}
		})
	}
	awaitJoined(t, group, "k", callers)
	close(release)
	wg.Wait()

	if got := runs.Load(); got != 1 {
		t.Fatalf("fn ran %d times for %d joined callers, want exactly 1", got, callers)
	}
	if got := group.InFlight(); got != 0 {
		t.Fatalf("InFlight()=%d after completion, want 0", got)
	}
}

// TestAbandonedLeaderDoesNotCondemnTheFollowers is the defect this package
// exists to avoid: the first caller is the one that has waited longest and so
// the one most likely to give up, and a naive implementation makes its
// cancellation everyone's. Internal because it needs the waiter barrier — a
// follower that had not yet joined would prove nothing.
//
// Goroutine lifecycle: two, the leader and the follower, each started here and
// terminated when its Do returns; both hand their result back on a buffered
// channel the test receives from, so neither outlives the assertions.
func TestAbandonedLeaderDoesNotCondemnTheFollowers(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	sharedCtxErr := make(chan error, 1)
	release := make(chan struct{})
	leaderCtx, cancelLeader := context.WithCancel(t.Context())

	leaderDone := make(chan error, 1)
	go func() {
		_, _, err := group.Do(leaderCtx, "k", func(callCtx context.Context) (int, error) {
			<-release
			//: the shared call must NOT have observed the leader's cancel.
			sharedCtxErr <- callCtx.Err()
			return 42, nil
		})
		leaderDone <- err
	}()
	awaitJoined(t, group, "k", 1)

	followerDone := make(chan int, 1)
	go func() {
		val, shared, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
			t.Error("the follower ran fn — the in-flight call was not joined")
			return 0, nil
		})
		if err != nil {
			t.Errorf("follower got %v, want nil", err)
		}
		if !shared {
			t.Error("follower reported shared=false")
		}
		followerDone <- val
	}()
	awaitJoined(t, group, "k", 2)

	cancelLeader()
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("the abandoning leader got %v, want context.Canceled", err)
	}
	close(release)

	if got := <-followerDone; got != 42 {
		t.Fatalf("follower got %d, want 42 — the leader's departure killed the shared call", got)
	}
	if err := <-sharedCtxErr; err != nil {
		t.Fatalf("shared call observed %v, want a live context — the leader's cancel leaked into it", err)
	}
}

// TestAbandonKeepsTheCallAliveUntilTheLastCallerLeaves pins the refcount rule
// directly: the shared context is cancelled on the LAST departure, not the
// first.
//
// Goroutine lifecycle: one per caller, started here and joined by wg.Wait;
// each terminates when its Do returns, which its own cancel() guarantees, and
// the one running fn additionally waits for release before returning.
func TestAbandonKeepsTheCallAliveUntilTheLastCallerLeaves(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	observed := make(chan struct{})
	release := make(chan struct{})
	const callers int = 3

	//: both slices are built BEFORE the goroutines so nothing per-iteration is
	//: captured by a closure — the goroutines index shared slices instead.
	callerCtx := make([]context.Context, callers)
	cancels := make([]context.CancelFunc, callers)
	for i := range callers {
		callerCtx[i], cancels[i] = context.WithCancel(t.Context())
	}

	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			//: every caller here abandons, so ctx.Err() is the expected
			//: return; the assertion is on what the SHARED call observed.
			_, _, err := group.Do(callerCtx[i], "k", func(callCtx context.Context) (int, error) {
				//: only the first caller's fn runs; it watches the shared ctx.
				<-callCtx.Done()
				close(observed)
				<-release
				return 0, nil
			})
			if !errors.Is(err, context.Canceled) {
				t.Errorf("an abandoning caller got %v, want context.Canceled", err)
			}
		})
	}
	awaitJoined(t, group, "k", callers)

	//: cancel all but the last; the shared call must survive every one.
	for i := range callers - 1 {
		cancels[i]()
	}
	//: the surviving caller keeps the refcount above zero, so the shared
	//: context must still be live — assert it by NOT observing the close.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		select {
		case <-observed:
			t.Fatal("shared call was cancelled while a caller was still waiting")
		default:
		}
		time.Sleep(time.Millisecond)
	}

	cancels[callers-1]()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("shared call was never cancelled after the last caller left")
	}
	close(release)
	wg.Wait()
}

// TestAwaitPrefersAnAvailableResultOverACancelledContext exercises the branch
// that cannot be reached deterministically from outside: a caller reaching the
// select with BOTH channels ready. A plain two-case select would return
// ctx.Err() roughly half the time, silently discarding an answer that exists.
func TestAwaitPrefersAnAvailableResultOverACancelledContext(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	entry := &call[string, int]{done: make(chan struct{}), cancel: func() {}, val: 9, key: "k"}
	entry.retired.Store(true)
	close(entry.done)

	//: repeated because the defect this guards against is probabilistic —
	//: one pass would be green half the time even with the branch removed.
	for attempt := range 1000 {
		entry.refs = 1
		val, shared, err := group.shardOf("k").await(ctx, entry, true)
		if err != nil {
			t.Fatalf("attempt %d returned %v, want the available result", attempt, err)
		}
		if val != 9 || !shared {
			t.Fatalf("attempt %d returned (%d, %t), want (9, true)", attempt, val, shared)
		}
	}
}

// TestPublishRetiresTheKeyBeforeWakingWaiters pins the ordering that keeps a
// newcomer from joining a call that has already produced its result: the map
// entry goes first, the wake-up second.
func TestPublishRetiresTheKeyBeforeWakingWaiters(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	entry := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	held := group.shardOf("k")
	held.calls = map[string]*call[string, int]{"k": entry}

	held.publish(entry)

	if _, found := held.calls["k"]; found {
		t.Fatal("publish left the key in the map — a newcomer could join a finished call")
	}
	if !entry.retired.Load() {
		t.Fatal("publish did not mark the call retired")
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("publish did not wake the waiters")
	}
}

// TestALosingLeaderCandidateIsCancelledAndNeverRuns pins #1035's race: a
// leader builds its call and context with the lock released, so another leader
// may insert the key in between. The loser must cancel the context it built,
// never start its fn, and join the winner — one execution, as before.
func TestALosingLeaderCandidateIsCancelledAndNeverRuns(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	held := group.shardOf("k")
	winner := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	held.calls = map[string]*call[string, int]{"k": winner}

	//: the candidate join would build, built the same way, and then settled
	//: against a map the winner reached first.
	candidate, candidateCtx := newCandidate[string, int](t.Context(), "k")
	var ran atomic.Int64
	entry, joined := held.settle(candidateCtx, candidate, hashOf("k"), func(context.Context) (int, error) {
		ran.Add(1)
		return 0, nil
	})
	if !joined || entry != winner {
		t.Fatalf("settle = (%p, %t), want the winner joined", entry, joined)
	}
	if winner.refs != 2 {
		t.Fatalf("winner refs = %d, want 2: the loser must be counted in", winner.refs)
	}
	if err := candidateCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("the losing candidate's context reads %v, want context.Canceled", err)
	}
	if held.calls["k"] != winner {
		t.Fatal("the losing candidate replaced the winner in the map")
	}
	if ran.Load() != 0 {
		t.Fatal("the losing candidate's fn ran")
	}
}

// TestManyLeadersRacingOnOneKeyRunFnOnce drives the window itself: 64
// goroutines start together on one key, so several build a candidate before
// any inserts; exactly one fn runs and every other caller joins it.
func TestManyLeadersRacingOnOneKeyRunFnOnce(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	release := make(chan struct{})
	var runs atomic.Int64
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			_, _, _ = group.Do(context.Background(), "k", func(context.Context) (int, error) {
				runs.Add(1)
				<-release
				return 1, nil
			})
		})
	}
	awaitJoined(t, group, "k", 64)
	close(release)
	wg.Wait()
	if got := runs.Load(); got != 1 {
		t.Fatalf("fn ran %d times for one key, want 1", got)
	}
}

// TestPublishDoesNotWaitForAHeldLock pins the publication #1035 takes off the
// lock: with the shard's mutex held elsewhere, publish still retires the call
// and wakes its waiters, and queues the call so the holder removes its map
// entry on its way out.
//
// Goroutine lifecycle: one, running publish, started here; it never blocks
// (that is the claim), and the test waits for it before any assertion.
func TestPublishDoesNotWaitForAHeldLock(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	held := group.shardOf("k")
	entry := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	held.calls = map[string]*call[string, int]{"k": entry}

	held.mu.Lock()
	published := make(chan struct{})
	go func() {
		held.publish(entry)
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		held.mu.Unlock()
		t.Fatal("publish waited for the shard's mutex")
	}
	select {
	case <-entry.done:
	default:
		held.mu.Unlock()
		t.Fatal("publish did not wake the waiters")
	}
	if !entry.retired.Load() {
		held.mu.Unlock()
		t.Fatal("publish did not mark the call retired")
	}
	if held.retired.Load() != entry {
		held.mu.Unlock()
		t.Fatal("publish did not queue the call for the lock's holder")
	}

	//: the holder leaves: the queued call's entry goes with it.
	held.unlock()
	if _, found := held.calls["k"]; found {
		t.Fatal("the holder left the retired call's entry in the map")
	}
	if held.retired.Load() != nil {
		t.Fatal("the holder left the queue undrained")
	}
	if entry.next != nil {
		t.Fatal("a drained call is still linked")
	}
}

// TestARetiredCallStillInTheMapIsNeitherJoinedNorCounted pins how a retired
// call whose entry awaits removal reads: absent. A newcomer leads a fresh
// call in its place, which the queued removal then leaves alone, and neither
// InFlight nor the waiter count sees the retired one.
func TestARetiredCallStillInTheMapIsNeitherJoinedNorCounted(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	held := group.shardOf("k")
	finished := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	finished.retired.Store(true)
	held.calls = map[string]*call[string, int]{"k": finished}

	if got := group.InFlight(); got != 0 {
		t.Fatalf("InFlight() = %d with only a retired call in the map, want 0", got)
	}
	if got := group.waiters("k"); got != 0 {
		t.Fatalf("waiters = %d on a retired call, want 0", got)
	}

	//: queued for removal, as publish leaves it when the lock was held.
	held.retired.Store(finished)
	candidate := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	winner, inserted := held.insertOrJoin(candidate, hashOf("k"))
	if !inserted || winner != candidate {
		t.Fatalf("insertOrJoin = (%p, %t), want the newcomer to lead", winner, inserted)
	}
	if finished.refs != 1 {
		t.Fatalf("the retired call's refs = %d, want 1: nobody may join it", finished.refs)
	}
	if held.calls["k"] != candidate {
		t.Fatal("draining the retired call removed the newcomer's entry")
	}
	if held.retired.Load() != nil {
		t.Fatal("the lock's holder left the queue undrained")
	}
	if got := group.InFlight(); got != 1 {
		t.Fatalf("InFlight() = %d, want 1: the newcomer's call", got)
	}
}

// TestAHotKeysFollowersJoinWithoutBuildingACandidate pins the first look:
// once a call has gained a follower, its key is the shard's hot key, and a
// later caller of that key joins the call in flight under the lock without
// building a candidate — the winner's refs grow, and no candidate is ever
// inserted in its place.
func TestAHotKeysFollowersJoinWithoutBuildingACandidate(t *testing.T) {
	t.Parallel()
	group := &Group[string, int]{}
	held := group.shardOf("k")
	winner := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	held.calls = map[string]*call[string, int]{"k": winner}

	//: a first follower, through the candidate path, makes "k" hot.
	candidate := &call[string, int]{done: make(chan struct{}), cancel: func() {}, refs: 1, key: "k"}
	if got, inserted := held.insertOrJoin(candidate, hashOf("k")); inserted || got != winner {
		t.Fatalf("insertOrJoin = (%p, %t), want the winner joined", got, inserted)
	}
	if held.hotHash.Load() != hashOf("k") {
		t.Fatal("a call that gained a follower did not make its key hot")
	}

	//: a nil context would panic in newCandidate: the first look must not
	//: reach it, so a nil one proves no candidate is built.
	var ran atomic.Int64
	var none context.Context
	entry, joined := held.join(none, "k", hashOf("k"), func(context.Context) (int, error) {
		ran.Add(1)
		return 0, nil
	})
	if !joined || entry != winner {
		t.Fatalf("join = (%p, %t), want the winner joined", entry, joined)
	}
	if winner.refs != 3 {
		t.Fatalf("winner refs = %d, want 3", winner.refs)
	}
	if ran.Load() != 0 {
		t.Fatal("a follower of the hot key ran fn")
	}
}

// TestABurstOfCallsLeavesNothingBehind is the retention check of the queue
// publish hands its removals to: after a burst of distinct, shared and
// abandoned calls on ONE shard, where publish meets a held lock as often as
// the scheduler allows, and then silence, the map is empty and nothing is
// queued — the shard keeps no finished call, so no result, alive.
//
// Goroutine lifecycle: one per worker, started here and joined by wg.Wait
// before any assertion; each returns after its last call. The goroutines the
// calls ran fn on have published before the waiters they served returned,
// and an abandoned call's publish takes no lock.
func TestABurstOfCallsLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	var held shard[string, int]
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	const workers, calls int = 9, 1500

	var wg sync.WaitGroup
	for worker := range workers {
		wg.Go(func() {
			for at := range calls {
				//: a third of the workers on keys of their own, a third on
				//: keys they share, a third leaving at once.
				key, ctx := strconv.Itoa(worker*calls+at), t.Context()
				switch worker % 3 {
				//: shared keys: the joiners' path.
				case 1:
					key = strconv.Itoa(at)
				//: already cancelled: the abandonment path.
				case 2:
					ctx = gone
				}
				entry, joined := held.join(ctx, key, hashOf(key), func(context.Context) (int, error) { return at, nil })
				_, _, err := held.await(ctx, entry, joined)
				if err != nil && ctx != gone {
					t.Errorf("Do(%q) = %v, want nil", key, err)
				}
			}
		})
	}
	wg.Wait()

	held.mu.Lock()
	left, queued := len(held.calls), held.retired.Load()
	held.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d entries left in the map after the burst, want 0", left)
	}
	if queued != nil {
		t.Fatal("a retired call is still queued after the burst")
	}
}
