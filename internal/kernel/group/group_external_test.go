package group_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/group"
)

// errTask is the test's own failure value. Production code may not build
// errors with errors.New (SDK rule 2); a test asserting that the group
// forwards the CALLER's error must have one to forward.
var errTask = errors.New("task failed")

// TestWaitReturnsOnlyAfterEveryTaskHasReturned pins the structured guarantee:
// when Wait leaves, the group owns no running goroutine. A group that returned
// early would hand back a "finished" set of tasks still writing to the
// caller's memory, which is the leak the primitive exists to prevent.
func TestWaitReturnsOnlyAfterEveryTaskHasReturned(t *testing.T) {
	t.Parallel()
	var running atomic.Int64
	var peak atomic.Int64
	g, _ := group.New(t.Context(), group.Unlimited)
	for range 16 {
		g.Go(func(context.Context) error {
			live := running.Add(1)
			for {
				top := peak.Load()
				if live <= top || peak.CompareAndSwap(top, live) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			running.Add(-1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if left := running.Load(); left != 0 {
		t.Fatalf("running after Wait = %d, want 0", left)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrency = %d, want the tasks to have overlapped", peak.Load())
	}
}

// TestFirstErrorIsReportedAndBecomesTheContextCause covers both halves of a
// failure: Wait reports the error that STARTED the shutdown, and the siblings
// can read WHY they are being stopped instead of only that they are.
func TestFirstErrorIsReportedAndBecomesTheContextCause(t *testing.T) {
	t.Parallel()
	g, ctx := group.New(t.Context(), group.Unlimited)
	observed := make(chan error, 1)
	g.Go(func(context.Context) error { return errTask })
	g.Go(func(taskCtx context.Context) error {
		<-taskCtx.Done()
		observed <- context.Cause(taskCtx)
		return taskCtx.Err()
	})
	if err := g.Wait(); !errors.Is(err, errTask) {
		t.Fatalf("Wait() = %v, want %v", err, errTask)
	}
	if cause := <-observed; !errors.Is(cause, errTask) {
		t.Fatalf("context.Cause = %v, want %v — a sibling must be able to tell a peer failure from a parent cancellation", cause, errTask)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errTask) {
		t.Fatalf("context.Cause(returned ctx) = %v, want %v", cause, errTask)
	}
}

// TestPanicInATaskReachesTheWaiterCarryingTheFailingStack is the reason this
// package exists rather than a five-line errgroup wrapper. An unrecovered
// panic in a child goroutine kills the process, and a stack captured in the
// waiter would point at code that did nothing wrong.
func TestPanicInATaskReachesTheWaiterCarryingTheFailingStack(t *testing.T) {
	t.Parallel()
	g, _ := group.New(t.Context(), group.Unlimited)
	g.Go(func(context.Context) error { return theTaskThatPanics() })
	defer func() {
		raised := recover()
		if raised == nil {
			t.Fatal("Wait() returned normally, want the task's panic re-raised")
		}
		value, ok := raised.(group.PanicValue)
		if !ok {
			t.Fatalf("recovered %T, want group.PanicValue", raised)
		}
		if got, want := value.Raised, "the task blew up"; got != want {
			t.Fatalf("PanicValue.Raised = %v, want %q", got, want)
		}
		if !strings.Contains(string(value.Stack), "theTaskThatPanics") {
			t.Fatalf("captured stack does not name the failing function:\n%s", value.Stack)
		}
		if !strings.Contains(value.String(), "kernel/group: panic in task:") {
			t.Fatalf("PanicValue.String() = %q, want it to name its own package", value.String())
		}
	}()
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want the deferred assertion above to have run first", err)
	}
}

// theTaskThatPanics exists only so the captured stack has a name the assertion
// above can look for.
func theTaskThatPanics() error {
	panic("the task blew up")
}

// TestPanicWaitsForEverySiblingBeforeReRaising pins the structured guarantee on
// the fault path too: Wait may panic, but not while a task is still running.
// The alternative would crash the process with live goroutines mid-write.
func TestPanicWaitsForEverySiblingBeforeReRaising(t *testing.T) {
	t.Parallel()
	var running atomic.Int64
	release := make(chan struct{})
	g, _ := group.New(t.Context(), group.Unlimited)
	g.Go(func(context.Context) error {
		running.Add(1)
		<-release
		running.Add(-1)
		return nil
	})
	g.Go(func(context.Context) error {
		close(release)
		panic("first")
	})
	defer func() {
		if recover() == nil {
			t.Fatal("Wait() returned normally, want a re-raised panic")
		}
		if left := running.Load(); left != 0 {
			t.Fatalf("running at re-raise = %d, want 0 — the sibling must finish first", left)
		}
	}()
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want the deferred assertion above to have run first", err)
	}
}

// TestNonPositiveLimitRunsSeriallyInsteadOfDeadlocking is ADR 0031 on this
// primitive: a zero limit is clamped to one, never read as "no task may run".
// The assertion is the OBSERVABLE outcome — the work ran, one task at a time —
// so it survives a change of mechanism.
func TestNonPositiveLimitRunsSeriallyInsteadOfDeadlocking(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, -1, -1000} {
		var live, peak atomic.Int64
		var completed atomic.Int64
		g, _ := group.New(t.Context(), limit)
		for range 8 {
			g.Go(func(context.Context) error {
				if now := live.Add(1); now > peak.Load() {
					peak.Store(now)
				}
				time.Sleep(time.Millisecond)
				live.Add(-1)
				completed.Add(1)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("limit %d: Wait() = %v, want nil", limit, err)
		}
		if completed.Load() != 8 {
			t.Fatalf("limit %d: completed = %d, want 8 — a clamped limit still runs the work", limit, completed.Load())
		}
		if peak.Load() != 1 {
			t.Fatalf("limit %d: peak concurrency = %d, want 1", limit, peak.Load())
		}
	}
}

// TestLimitBoundsConcurrencyBeforeTheGoroutineExists checks the bound is taken
// on the submitting goroutine: 64 submissions against a limit of 4 must never
// have more than 4 tasks live.
func TestLimitBoundsConcurrencyBeforeTheGoroutineExists(t *testing.T) {
	t.Parallel()
	const limit int = 4
	var live, peak atomic.Int64
	g, _ := group.New(t.Context(), limit)
	for range 64 {
		g.Go(func(context.Context) error {
			now := live.Add(1)
			for {
				top := peak.Load()
				if now <= top || peak.CompareAndSwap(top, now) {
					break
				}
			}
			time.Sleep(100 * time.Microsecond)
			live.Add(-1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if peak.Load() > int64(limit) {
		t.Fatalf("peak concurrency = %d, want at most %d", peak.Load(), limit)
	}
}

// TestUnlimitedIsUsableAndCostsNoMemory guards the implementation choice behind
// the Unlimited constant: the semaphore is a chan struct{}, whose buffer is
// free at any size, so math.MaxInt is a legal capacity rather than an
// allocation the size of the address space.
func TestUnlimitedIsUsableAndCostsNoMemory(t *testing.T) {
	t.Parallel()
	var done atomic.Int64
	g, _ := group.New(t.Context(), group.Unlimited)
	for range 32 {
		g.Go(func(context.Context) error {
			done.Add(1)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
	if done.Load() != 32 {
		t.Fatalf("completed = %d, want 32", done.Load())
	}
}

// TestATaskSubmittedAfterAFailureStillRuns records the deliberate choice: a
// late submission is not silently skipped. It runs with an already-cancelled
// context and a cooperative task returns at once — which is observable,
// whereas a task that never ran and never said so is not.
func TestATaskSubmittedAfterAFailureStillRuns(t *testing.T) {
	t.Parallel()
	g, _ := group.New(t.Context(), 1)
	g.Go(func(context.Context) error { return errTask })
	var ran, sawCancellation atomic.Bool
	g.Go(func(taskCtx context.Context) error {
		ran.Store(true)
		sawCancellation.Store(taskCtx.Err() != nil)
		return nil
	})
	if err := g.Wait(); !errors.Is(err, errTask) {
		t.Fatalf("Wait() = %v, want %v", err, errTask)
	}
	if !ran.Load() {
		t.Fatal("the late task did not run — a silent skip is exactly what this group refuses")
	}
	if !sawCancellation.Load() {
		t.Fatal("the late task saw a live context, want it already cancelled")
	}
}

// TestWaitReturnsNilWhenNothingWasSubmitted keeps the empty case honest: a
// group with no tasks is finished, not broken.
func TestWaitReturnsNilWhenNothingWasSubmitted(t *testing.T) {
	t.Parallel()
	g, _ := group.New(t.Context(), 4)
	if err := g.Wait(); err != nil {
		t.Fatalf("Wait() = %v, want nil", err)
	}
}

// TestCollectReturnsResultsInSubmissionOrder is the one guarantee that makes
// Collect usable: index i belongs to fns[i], whatever order the tasks finished
// in. The delays below are reversed on purpose so completion order cannot
// accidentally match submission order.
func TestCollectReturnsResultsInSubmissionOrder(t *testing.T) {
	t.Parallel()
	fns := make([]func(context.Context) (int, error), 8)
	for i := range fns {
		delay := time.Duration(len(fns)-i) * time.Millisecond
		fns[i] = func(context.Context) (int, error) {
			time.Sleep(delay)
			return i * 10, nil
		}
	}
	out, err := group.Collect(t.Context(), group.Unlimited, fns)
	if err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	for i, got := range out {
		if want := i * 10; got != want {
			t.Fatalf("out[%d] = %d, want %d — results must follow submission order", i, got, want)
		}
	}
}

// TestCollectDiscardsPartialResultsOnError pins the refusal to hand back a
// half-filled slice: a zero in it would read as an answer.
func TestCollectDiscardsPartialResultsOnError(t *testing.T) {
	t.Parallel()
	fns := []func(context.Context) (string, error){
		func(context.Context) (string, error) { return "first", nil },
		func(context.Context) (string, error) { return "", errTask },
		func(context.Context) (string, error) { return "third", nil },
	}
	out, err := group.Collect(t.Context(), group.Unlimited, fns)
	if !errors.Is(err, errTask) {
		t.Fatalf("Collect() error = %v, want %v", err, errTask)
	}
	if out != nil {
		t.Fatalf("Collect() = %v, want nil — a partial result set is indistinguishable from a complete one", out)
	}
}

// TestCollectOnAnEmptyInputIsNotAFailure keeps the degenerate case from
// allocating, spawning, or erroring.
func TestCollectOnAnEmptyInputIsNotAFailure(t *testing.T) {
	t.Parallel()
	out, err := group.Collect[int](t.Context(), 4, nil)
	if err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	if out != nil {
		t.Fatalf("Collect() = %v, want nil", out)
	}
}

// TestCollectHonoursTheLimit checks the clamp reaches Collect too — it is the
// same New underneath, and a Collect that ignored the bound would be a second,
// unbounded way in.
func TestCollectHonoursTheLimit(t *testing.T) {
	t.Parallel()
	var live, peak atomic.Int64
	fns := make([]func(context.Context) (int, error), 32)
	for i := range fns {
		fns[i] = func(context.Context) (int, error) {
			now := live.Add(1)
			for {
				top := peak.Load()
				if now <= top || peak.CompareAndSwap(top, now) {
					break
				}
			}
			time.Sleep(100 * time.Microsecond)
			live.Add(-1)
			return i, nil
		}
	}
	if _, err := group.Collect(t.Context(), 3, fns); err != nil {
		t.Fatalf("Collect() error = %v, want nil", err)
	}
	if peak.Load() > 3 {
		t.Fatalf("peak concurrency = %d, want at most 3", peak.Load())
	}
}

// TestParentCancellationReachesEveryTask checks the derived context really is
// derived: cancelling the parent must stop cooperative tasks.
func TestParentCancellationReachesEveryTask(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(t.Context())
	g, _ := group.New(parent, group.Unlimited)
	started := make(chan struct{}, 4)
	for range 4 {
		g.Go(func(taskCtx context.Context) error {
			started <- struct{}{}
			<-taskCtx.Done()
			return taskCtx.Err()
		})
	}
	for range 4 {
		<-started
	}
	cancel()
	if err := g.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() = %v, want context.Canceled", err)
	}
}
