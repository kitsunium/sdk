package singleflight

import (
	"context"
	"errors"
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
	g.mu.RLock()
	defer g.mu.RUnlock()
	//: an absent key has no waiters.
	entry, found := g.calls[key]
	if !found {
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

	entry := &call[int]{done: make(chan struct{}), cancel: func() {}, val: 9, retired: true}
	close(entry.done)

	//: repeated because the defect this guards against is probabilistic —
	//: one pass would be green half the time even with the branch removed.
	for attempt := range 1000 {
		entry.refs = 1
		val, shared, err := group.await(ctx, "k", entry, true)
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
	entry := &call[int]{done: make(chan struct{}), cancel: func() {}, refs: 1}
	group.calls = map[string]*call[int]{"k": entry}

	group.publish("k", entry)

	if _, found := group.calls["k"]; found {
		t.Fatal("publish left the key in the map — a newcomer could join a finished call")
	}
	if !entry.retired {
		t.Fatal("publish did not mark the call retired")
	}
	select {
	case <-entry.done:
	default:
		t.Fatal("publish did not wake the waiters")
	}
}
