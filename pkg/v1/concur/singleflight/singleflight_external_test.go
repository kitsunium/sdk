package singleflight_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/kitsunium/sdk/pkg/v1/concur/singleflight"
)

// TestConcurrentCallersShareOneExecution asserts the contract through the
// public alias: callers that arrive while a call for their key is in flight
// join it — one execution, one value, every joiner told it was shared — and the
// key retires once the call is done. synctest makes "arrived while in flight"
// a fact rather than a timing guess: Wait returns only once every caller is
// durably blocked inside Do.
func TestConcurrentCallersShareOneExecution(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls singleflight.Group[string, int]
		var runs atomic.Int64
		release := make(chan struct{})
		const callers int = 8
		values := make([]int, callers)
		shared := make([]bool, callers)
		var wg sync.WaitGroup
		do := func(i int) {
			v, s, err := calls.Do(context.Background(), "k", func(context.Context) (int, error) {
				runs.Add(1)
				<-release
				return 42, nil
			})
			if err != nil {
				t.Errorf("caller %d: Do = %v", i, err)
			}
			values[i], shared[i] = v, s
		}
		wg.Go(func() { do(0) })
		synctest.Wait() // the leader's call is in flight and blocked on release
		for i := 1; i < callers; i++ {
			wg.Go(func() { do(i) })
		}
		synctest.Wait() // every follower has joined it
		if got := calls.InFlight(); got != 1 {
			t.Errorf("InFlight = %d while the call runs, want 1", got)
		}
		close(release)
		wg.Wait()
		if runs.Load() != 1 {
			t.Errorf("fn ran %d times for %d concurrent callers, want 1", runs.Load(), callers)
		}
		for i := range callers {
			if values[i] != 42 || shared[i] != (i > 0) {
				t.Errorf("caller %d got (%d, shared %v), want (42, shared %v)", i, values[i], shared[i], i > 0)
			}
		}
		if got := calls.InFlight(); got != 0 {
			t.Errorf("InFlight = %d after the call, want 0", got)
		}
	})
}

// TestForgetStartsAFreshCall asserts that a key forgotten while its call is in
// flight is started again by the next caller, while the first call still
// answers the caller already waiting on it.
func TestForgetStartsAFreshCall(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var calls singleflight.Group[string, string]
		release := make(chan struct{})
		var first string
		var firstErr error
		var wg sync.WaitGroup
		wg.Go(func() {
			first, _, firstErr = calls.Do(context.Background(), "k", func(context.Context) (string, error) {
				<-release
				return "stale", nil
			})
		})
		synctest.Wait()
		calls.Forget("k")
		fresh, shared, err := calls.Do(context.Background(), "k", func(context.Context) (string, error) {
			return "fresh", nil
		})
		close(release)
		wg.Wait()
		if fresh != "fresh" || shared || err != nil {
			t.Errorf("Do after Forget = (%q, shared %v, %v), want a fresh call of its own", fresh, shared, err)
		}
		if first != "stale" || firstErr != nil {
			t.Errorf("the forgotten call answered (%q, %v) to its caller, want (%q, nil)", first, firstErr, "stale")
		}
	})
}

// TestAPanicReachesTheCallerAsAPanicValue asserts the panic is re-raised in
// the caller as the public PanicValue, carrying the value and fn's stack.
func TestAPanicReachesTheCallerAsAPanicValue(t *testing.T) {
	t.Parallel()
	var calls singleflight.Group[int, int]
	defer func() {
		pv, ok := recover().(singleflight.PanicValue)
		if !ok {
			t.Fatal("Do did not re-raise a singleflight.PanicValue")
		}
		if pv.Raised != "boom" || len(pv.Stack) == 0 {
			t.Errorf("PanicValue = {Raised: %v, %d stack bytes}, want boom and a stack", pv.Raised, len(pv.Stack))
		}
	}()
	_, _, err := calls.Do(t.Context(), 1, func(context.Context) (int, error) { panic("boom") })
	t.Errorf("Do returned %v instead of re-raising the panic", err)
}

// ExampleGroup shows the zero value in use: one call for the key, its result
// returned to the caller.
func ExampleGroup() {
	var lookups singleflight.Group[string, int]
	n, shared, err := lookups.Do(context.Background(), "answer", func(context.Context) (int, error) {
		return 42, nil
	})
	fmt.Println(n, shared, err)
	// Output: 42 false <nil>
}
