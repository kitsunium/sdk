package group_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/concur/group"
)

// errOne and errTwo are the failures the tasks below return. Plain stdlib
// errors are right here: a test fixture, and the group passes a task's error
// through untouched.
var (
	errOne = errors.New("one")
	errTwo = errors.New("two")
)

// TestNewReportsTheFirstFailureAndCancelsTheRest asserts New's contract
// through the public names: the first failure is what Wait returns and the
// context's cause, and a sibling sees that context cancelled.
func TestNewReportsTheFirstFailureAndCancelsTheRest(t *testing.T) {
	t.Parallel()
	g, ctx := group.New(t.Context(), 2)
	g.Go(func(context.Context) error { return errOne })
	g.Go(func(ctx context.Context) error {
		<-ctx.Done() // ends only because the first task failed
		return context.Cause(ctx)
	})
	if err := g.Wait(); !errors.Is(err, errOne) {
		t.Errorf("Wait = %v, want the first failure %v", err, errOne)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errOne) {
		t.Errorf("context.Cause = %v, want %v", cause, errOne)
	}
}

// TestNewJoinedReportsEveryFailure asserts that a joined group returns every
// task's error, each matchable, and nil — not an empty join — when none
// failed.
func TestNewJoinedReportsEveryFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		tasks []error
		want  []error
	}{
		{"two failures and a success", []error{errOne, nil, errTwo}, []error{errOne, errTwo}},
		{"no failure", []error{nil, nil}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g, _ := group.NewJoined(t.Context(), group.Unlimited)
			for _, failure := range tc.tasks {
				g.Go(func(context.Context) error { return failure })
			}
			err := g.Wait()
			if tc.want == nil && err != nil {
				t.Errorf("Wait = %v, want nil", err)
			}
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Errorf("Wait = %v, does not carry %v", err, want)
				}
			}
		})
	}
}

// TestCollectKeepsSubmissionOrder makes the tasks finish in reverse order —
// each waits for the one submitted after it — and asserts the results still
// come back in the order they were submitted.
func TestCollectKeepsSubmissionOrder(t *testing.T) {
	t.Parallel()
	const tasks int = 4
	finished := make([]chan struct{}, tasks+1)
	for i := range finished {
		finished[i] = make(chan struct{})
	}
	close(finished[tasks]) // the last task waits for nobody
	fns := make([]func(context.Context) (int, error), tasks)
	for i := range fns {
		fns[i] = func(context.Context) (int, error) {
			<-finished[i+1]
			defer close(finished[i])
			return i * 10, nil
		}
	}
	got, err := group.Collect(t.Context(), group.Unlimited, fns)
	if err != nil || !slices.Equal(got, []int{0, 10, 20, 30}) {
		t.Errorf("Collect = %v, %v; want [0 10 20 30], nil", got, err)
	}
}

// TestAPanicReachesTheWaiterAsAPanicValue asserts the panic is re-raised in
// Wait as the public PanicValue — the alias is the kernel's type, so the
// assertion a consumer writes succeeds — carrying the value and a stack.
func TestAPanicReachesTheWaiterAsAPanicValue(t *testing.T) {
	t.Parallel()
	g, _ := group.New(t.Context(), 1)
	g.Go(func(context.Context) error { panic("boom") })
	defer func() {
		pv, ok := recover().(group.PanicValue)
		if !ok {
			t.Fatal("Wait did not re-raise a group.PanicValue")
		}
		if pv.Raised != "boom" || len(pv.Stack) == 0 {
			t.Errorf("PanicValue = {Raised: %v, %d stack bytes}, want boom and a stack", pv.Raised, len(pv.Stack))
		}
	}()
	err := g.Wait()
	t.Errorf("Wait returned %v instead of re-raising the panic", err)
}

// TestTheLimitBoundsTheTasksRunningAtOnce asserts both ends of the limit: a
// zero limit is clamped to one task at a time, and Unlimited lets every task
// run at once — proved with a barrier no task passes until all have started.
func TestTheLimitBoundsTheTasksRunningAtOnce(t *testing.T) {
	t.Parallel()
	t.Run("a zero limit runs one task at a time", func(t *testing.T) {
		t.Parallel()
		var running, peak atomic.Int64
		g, _ := group.New(t.Context(), 0)
		for range 8 {
			g.Go(func(context.Context) error {
				notePeak(&peak, running.Add(1))
				runtime.Gosched()
				running.Add(-1)
				return nil
			})
		}
		if err := g.Wait(); err != nil || peak.Load() != 1 {
			t.Errorf("Wait = %v, peak = %d; want nil and 1", err, peak.Load())
		}
	})
	t.Run("Unlimited runs every task at once", func(t *testing.T) {
		t.Parallel()
		const tasks int64 = 8
		var started atomic.Int64
		allIn := make(chan struct{})
		g, _ := group.New(t.Context(), group.Unlimited)
		for range tasks {
			g.Go(func(context.Context) error {
				if started.Add(1) == tasks {
					close(allIn)
				}
				<-allIn // a bound below the task count would never open this barrier
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			t.Errorf("Wait = %v, want nil", err)
		}
	})
}

// notePeak raises peak to now when now is higher, against concurrent
// raisers.
func notePeak(peak *atomic.Int64, now int64) {
	for {
		p := peak.Load()
		if now <= p || peak.CompareAndSwap(p, now) {
			return
		}
	}
}

// ExampleCollect fans three computations out and reads their results in the
// order they were submitted.
func ExampleCollect() {
	words := []string{"alpha", "beta", "gamma"}
	fns := make([]func(context.Context) (int, error), len(words))
	for i, w := range words {
		fns[i] = func(context.Context) (int, error) { return len(w), nil }
	}
	lengths, err := group.Collect(context.Background(), group.Unlimited, fns)
	fmt.Println(lengths, err)
	// Output: [5 4 5] <nil>
}

// ExampleNewJoined runs two independent tasks that both fail and reports both
// failures, in submission order.
func ExampleNewJoined() {
	g, _ := group.NewJoined(context.Background(), 2)
	g.Go(func(context.Context) error { return errors.New("disk full") })
	g.Go(func(context.Context) error { return errors.New("network down") })
	fmt.Println(g.Wait())
	// Output:
	// disk full
	// network down
}
