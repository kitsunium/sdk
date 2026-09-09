package singleflight_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/singleflight"
)

// errBoom is the sentinel a failing fn returns. A plain stdlib error is right
// here: this is a test fixture, not SDK production code, and the primitive is
// deliberately transparent to whatever error fn produces.
var errBoom = errors.New("boom")

// ctxKey is the context key used to prove which caller's VALUES the shared
// call inherits.
type ctxKey struct{}

// waitFor spins until cond holds or the test deadline is reached. Used instead
// of a sleep so the assertions do not encode a timing guess.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestConcurrentCallersOnOneKeyShareOneResult asserts the contract a consumer
// sees: every caller gets the same value, a caller that joined says so, fewer
// executions happen than there were callers, and the key retires.
//
// The STRICT claim — exactly one execution — is asserted in the internal test
// instead, because it needs a barrier on the group's waiter count. From
// outside, a goroutine that arrives after the first call has already published
// legitimately starts a second one, so a strict count here would be a test of
// the scheduler rather than of the primitive.
//
// Goroutine lifecycle: one per caller, started here and joined by wg.Wait
// before any assertion runs; each terminates when Do returns, which the closing
// of release guarantees.
func TestConcurrentCallersOnOneKeyShareOneResult(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	var runs atomic.Int64
	release := make(chan struct{})
	const callers int = 32

	var wg sync.WaitGroup
	shared := make([]bool, callers)
	values := make([]int, callers)
	for i := range callers {
		wg.Go(func() {
			val, wasShared, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
				runs.Add(1)
				<-release
				return 7, nil
			})
			if err != nil {
				t.Errorf("Do returned %v, want nil", err)
			}
			values[i], shared[i] = val, wasShared
		})
	}
	waitFor(t, func() bool { return group.InFlight() == 1 }, "the call to be in flight")
	close(release)
	wg.Wait()

	leaders := 0
	for i := range callers {
		if values[i] != 7 {
			t.Fatalf("caller %d got %d, want 7", i, values[i])
		}
		if !shared[i] {
			leaders++
		}
	}
	if leaders != int(runs.Load()) {
		t.Fatalf("%d callers reported shared=false but fn ran %d times — the flag and the work disagree", leaders, runs.Load())
	}
	if leaders >= callers {
		t.Fatalf("every one of the %d callers led its own call — nothing was deduplicated", callers)
	}
	if got := group.InFlight(); got != 0 {
		t.Fatalf("InFlight()=%d after completion, want 0", got)
	}
}

// TestDifferentKeysDoNotWaitForEachOther proves a Group is not a lock: two
// distinct keys must be able to be inside fn at the SAME time.
//
// Goroutine lifecycle: one per key, started here and joined by wg.Wait; each
// terminates when Do returns, which the closing of release guarantees. A
// serialising implementation does not fail an assertion here — it deadlocks,
// and the select's own 2s timeout is what turns that into a named failure.
func TestDifferentKeysDoNotWaitForEachOther(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, string]
	entered := make(chan string, 2)
	release := make(chan struct{})

	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Go(func() {
			if _, _, err := group.Do(t.Context(), key, func(context.Context) (string, error) {
				entered <- key
				<-release
				return key, nil
			}); err != nil {
				t.Errorf("Do(%q) returned %v, want nil", key, err)
			}
		})
	}
	//: both must be inside fn at once; a mutex-shaped implementation blocks
	//: here rather than returning a wrong answer.
	seen := map[string]bool{}
	for range 2 {
		select {
		case key := <-entered:
			seen[key] = true
		case <-time.After(2 * time.Second):
			t.Fatal("second key never entered fn — keys are serialising each other")
		}
	}
	close(release)
	wg.Wait()
	if len(seen) != 2 {
		t.Fatalf("entered fn for %v, want both keys", seen)
	}
}

func TestSequentialCallsAreNotCached(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	var runs atomic.Int64
	for range 3 {
		if _, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
			runs.Add(1)
			return 1, nil
		}); err != nil {
			t.Fatalf("Do returned %v, want nil", err)
		}
	}
	if got := runs.Load(); got != 3 {
		t.Fatalf("fn ran %d times, want 3 — a Group is not a cache", got)
	}
}

// TestLastCallerToLeaveCancelsTheSharedCall pins the other half of the
// abandonment rule: nothing is computed for an audience of zero.
//
// Goroutine lifecycle: exactly one, started here and terminated when Do
// returns — which happens once fn observes the shared cancellation. The
// select's 2s timeout bounds the failure if it never does.
func TestLastCallerToLeaveCancelsTheSharedCall(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	observed := make(chan error, 1)
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		//: the caller abandons, so ctx.Err() is the expected return; the
		//: subject of the test is what the SHARED call saw, on observed.
		_, _, err := group.Do(ctx, "k", func(callCtx context.Context) (int, error) {
			close(started)
			//: block until the group withdraws the shared context.
			<-callCtx.Done()
			observed <- callCtx.Err()
			return 0, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the abandoning caller got %v, want context.Canceled", err)
		}
	}()
	<-started
	cancel()

	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("shared call saw %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared context was never cancelled after the last caller left")
	}
}

func TestSharedCallKeepsLeaderValuesAndDropsLeaderDeadline(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, string]
	leaderCtx := context.WithValue(t.Context(), ctxKey{}, "leader")
	leaderCtx, cancel := context.WithTimeout(leaderCtx, time.Hour)
	defer cancel()

	got, _, err := group.Do(leaderCtx, "k", func(ctx context.Context) (string, error) {
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			t.Error("shared call inherited the leader's deadline")
		}
		value, _ := ctx.Value(ctxKey{}).(string)
		return value, nil
	})
	if err != nil {
		t.Fatalf("Do returned %v, want nil", err)
	}
	if got != "leader" {
		t.Fatalf("shared call saw value %q, want %q", got, "leader")
	}
}

// TestErrorFromFnReachesEveryCaller: the Group is transparent to fn's error.
//
// Goroutine lifecycle: one per caller, started here and joined by wg.Wait
// before the assertions; each terminates when Do returns, which the closing of
// release guarantees.
func TestErrorFromFnReachesEveryCaller(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	release := make(chan struct{})
	var wg sync.WaitGroup
	seen := make([]error, 8)
	for i := range seen {
		wg.Go(func() {
			_, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
				<-release
				return 0, errBoom
			})
			seen[i] = err
		})
	}
	waitFor(t, func() bool { return group.InFlight() == 1 }, "the call to start")
	close(release)
	wg.Wait()
	for i, err := range seen {
		if !errors.Is(err, errBoom) {
			t.Fatalf("caller %d got %v, want errBoom", i, err)
		}
	}
}

// TestPanicIsReRaisedInEveryWaiterWithTheOriginatingStack: a waiter must never
// be left blocked on a channel that will never close, and must never receive a
// zero value that looks like a legitimate answer.
//
// Goroutine lifecycle: one per caller, started here and joined by wg.Wait;
// each terminates by PANICKING out of Do into its own deferred recover, which
// is the behaviour under test.
func TestPanicIsReRaisedInEveryWaiterWithTheOriginatingStack(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	release := make(chan struct{})
	const callers int = 4
	var wg sync.WaitGroup
	recovered := make([]any, callers)
	for i := range callers {
		wg.Go(func() {
			defer func() { recovered[i] = recover() }()
			_, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
				<-release
				panic("fn exploded")
			})
			//: unreachable while the contract holds — Do panics rather than
			//: returning, and a return here IS the regression.
			t.Errorf("Do returned %v instead of re-raising the panic", err)
		})
	}
	waitFor(t, func() bool { return group.InFlight() == 1 }, "the call to start")
	close(release)
	wg.Wait()

	for i, raised := range recovered {
		panicValue, ok := raised.(singleflight.PanicValue)
		if !ok {
			t.Fatalf("caller %d recovered %#v, want a singleflight.PanicValue", i, raised)
		}
		if panicValue.Raised != "fn exploded" {
			t.Fatalf("caller %d recovered payload %#v, want the original value", i, panicValue.Raised)
		}
		//: the stack must name the goroutine that ran fn, not the waiter's.
		if !strings.Contains(string(panicValue.Stack), "singleflight_external_test.go") {
			t.Fatalf("caller %d got a stack that does not name the failing frame:\n%s", i, panicValue.Stack)
		}
		if rendered := panicValue.String(); !strings.Contains(rendered, "fn exploded") {
			t.Fatalf("caller %d String() = %q, want it to carry the original panic", i, rendered)
		}
	}
	//: a panicking call must still retire its key, or the next caller blocks
	//: on a channel that will never close.
	if got := group.InFlight(); got != 0 {
		t.Fatalf("InFlight()=%d after a panic, want 0", got)
	}
	if _, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("the key was not usable after a panic: %v", err)
	}
}

// TestForgetLetsTheNextCallerStartFreshWithoutAbandoningTheCurrentOne:
// forgetting a key drops the dedup entry, it does not abandon the work.
//
// Goroutine lifecycle: two, one per call, each started here and terminated
// when its Do returns — which the closing of release guarantees. Each hands
// its result back on a buffered channel the test then receives from, so
// neither can outlive the assertions.
func TestForgetLetsTheNextCallerStartFreshWithoutAbandoningTheCurrentOne(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	var runs atomic.Int64
	release := make(chan struct{})

	first := make(chan int, 1)
	go func() {
		val, _, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
			runs.Add(1)
			<-release
			return 1, nil
		})
		if err != nil {
			t.Errorf("the first caller got %v, want nil", err)
		}
		first <- val
	}()
	waitFor(t, func() bool { return group.InFlight() == 1 }, "the first call to start")

	group.Forget("k")

	second := make(chan int, 1)
	go func() {
		val, shared, err := group.Do(t.Context(), "k", func(context.Context) (int, error) {
			runs.Add(1)
			<-release
			return 2, nil
		})
		if err != nil {
			t.Errorf("the second caller got %v, want nil", err)
		}
		if shared {
			t.Error("the caller after Forget joined the forgotten call")
		}
		second <- val
	}()
	waitFor(t, func() bool { return group.InFlight() == 1 }, "the second call to start")
	close(release)

	if got := <-first; got != 1 {
		t.Fatalf("the first caller got %d, want 1 — Forget abandoned its work", got)
	}
	if got := <-second; got != 2 {
		t.Fatalf("the second caller got %d, want 2", got)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("fn ran %d times, want 2 — Forget must start a genuinely new call", got)
	}
}

func TestCallerWithAnAlreadyDoneContextGetsItsOwnCancellation(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[string, int]
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	release := make(chan struct{})
	defer close(release)

	_, _, err := group.Do(ctx, "k", func(context.Context) (int, error) {
		<-release
		return 1, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
}

func TestZeroGroupIsUsable(t *testing.T) {
	t.Parallel()
	var group singleflight.Group[int, string]
	got, shared, err := group.Do(t.Context(), 1, func(context.Context) (string, error) { return "ok", nil })
	if err != nil || shared || got != "ok" {
		t.Fatalf("Do on a zero Group = (%q, %t, %v), want (\"ok\", false, nil)", got, shared, err)
	}
}
