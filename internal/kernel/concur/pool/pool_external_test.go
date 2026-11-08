package pool_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/concur/pool"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// waitGoroutines polls until the process runs at most want goroutines.
func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	for range 500 {
		//: an exiting goroutine leaves on its own schedule.
		if runtime.NumGoroutine() <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, want at most %d", runtime.NumGoroutine(), want)
}

// TestSubmitWithoutAQueueRefusesAtOnce is #804's: with Queue 0 and every
// worker busy, Submit returns PoolFull without waiting — its context, which
// would bound a wait, never ends, and the call returns all the same.
func TestSubmitWithoutAQueueRefusesAtOnce(t *testing.T) {
	t.Parallel()
	p := pool.NewPool(context.Background(), pool.Config{Workers: 2})
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)
	for range 2 {
		for {
			err := p.Submit(context.Background(), func(context.Context) error {
				started.Done()
				<-release
				return nil
			})
			//: a worker that has not reached its receive yet refuses: retry.
			if err == nil {
				break
			}
			runtime.Gosched()
		}
	}
	started.Wait()
	refused := make(chan error, 1)
	go func() {
		refused <- p.Submit(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case err := <-refused:
		if !errs.HasCode(err, pool.CodePoolFull) {
			t.Errorf("Submit with every worker busy = %v, want PoolFull", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Submit parked instead of refusing")
	}
	close(release)
	if err := p.Drain(t.Context()); err != nil {
		t.Errorf("Drain: %v", err)
	}
}

// TestSubmitWithAQueueWaitsWithinItsContext: a full queue is waited on, and
// the wait ends with the submitter's context as PoolFull wrapping its error.
func TestSubmitWithAQueueWaitsWithinItsContext(t *testing.T) {
	t.Parallel()
	p := pool.NewPool(context.Background(), pool.Config{Workers: 1, Queue: 1})
	release := make(chan struct{})
	block := func(context.Context) error {
		<-release
		return nil
	}
	//: one running, one queued: the next Submit finds no room.
	for i := 0; i < 2; {
		if p.Submit(context.Background(), block) == nil {
			i++
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.Submit(ctx, block)
	if !errs.HasCode(err, pool.CodePoolFull) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Submit on a full queue = %v, want PoolFull wrapping DeadlineExceeded", err)
	}
	close(release)
	if err := p.Drain(t.Context()); err != nil {
		t.Errorf("Drain: %v", err)
	}
}

// TestPanicReachesOnPanicAndTheWorkerServesOn is #804's: a task's panic
// reaches OnPanic with the stack of the worker that ran it, and that worker —
// the only one — runs the next task.
func TestPanicReachesOnPanicAndTheWorkerServesOn(t *testing.T) {
	t.Parallel()
	panics := make(chan pool.PanicValue, 1)
	p := pool.NewPool(context.Background(), pool.Config{
		Workers: 1, Queue: 4,
		OnPanic: func(pv pool.PanicValue) { panics <- pv },
	})
	if err := p.Submit(t.Context(), func(context.Context) error { panicInTask(); return nil }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	ran := make(chan struct{})
	if err := p.Submit(t.Context(), func(context.Context) error { close(ran); return nil }); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	pv := <-panics
	if pv.Raised != "task fault" {
		t.Errorf("Raised = %v, want the task's value", pv.Raised)
	}
	if !strings.Contains(string(pv.Stack), "panicInTask") {
		t.Errorf("the stack is not the task's:\n%s", pv.Stack)
	}
	if !strings.Contains(pv.String(), "task fault") {
		t.Errorf("String() = %q", pv.String())
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not serve the task after the panic")
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Errorf("Drain: %v", err)
	}
}

// panicInTask is a named frame, so the stack OnPanic receives can be checked
// for it.
//
//go:noinline
func panicInTask() {
	panic("task fault")
}

// TestDrainRunsTheQueueAndLeavesNoGoroutine is #804's: Drain returns only
// once every queued task has run, and the process is back to the goroutines
// it had before New. Not parallel: it counts the process's goroutines.
func TestDrainRunsTheQueueAndLeavesNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	p := pool.NewPool(context.Background(), pool.Config{Workers: 4, Queue: 64})
	var ran atomic.Int64
	const tasks = 64
	for range tasks {
		err := p.Submit(t.Context(), func(context.Context) error {
			time.Sleep(time.Millisecond)
			ran.Add(1)
			return nil
		})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := ran.Load(); got != tasks {
		t.Errorf("Drain returned with %d of %d tasks run", got, tasks)
	}
	if err := p.Submit(t.Context(), func(context.Context) error { return nil }); !errs.HasCode(err, pool.CodePoolClosed) {
		t.Errorf("Submit after Drain = %v, want PoolClosed", err)
	}
	waitGoroutines(t, before)
}

// TestDrainWithinItsContext: a task that outlasts Drain's context makes it
// return that context's error; Stop then cancels the task's context, abandons
// the queue and joins.
func TestDrainWithinItsContextThenStop(t *testing.T) {
	t.Parallel()
	p := pool.NewPool(context.Background(), pool.Config{Workers: 1, Queue: 8})
	started := make(chan struct{})
	if err := p.Submit(t.Context(), func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	var abandoned atomic.Int64
	for range 4 {
		if err := p.Submit(t.Context(), func(context.Context) error { abandoned.Add(1); return nil }); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain past its deadline = %v, want DeadlineExceeded", err)
	}
	if err := p.Stop(t.Context()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if abandoned.Load() != 0 {
		t.Errorf("%d abandoned tasks ran after Stop", abandoned.Load())
	}
}

// TestTaskErrorGoesToOnError: a task's error has no caller and reaches
// OnError.
func TestTaskErrorGoesToOnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	got := make(chan error, 1)
	p := pool.NewPool(context.Background(), pool.Config{OnError: func(err error) { got <- err }})
	for p.Submit(t.Context(), func(context.Context) error { return boom }) != nil {
		runtime.Gosched()
	}
	if err := <-got; !errors.Is(err, boom) {
		t.Errorf("OnError got %v, want boom", err)
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Errorf("Drain: %v", err)
	}
}

// TestConcurrentSubmitAndDrain: submitters racing Drain either have their
// task run or are refused with PoolClosed — none is accepted and lost.
func TestConcurrentSubmitAndDrain(t *testing.T) {
	t.Parallel()
	p := pool.NewPool(context.Background(), pool.Config{Workers: 4, Queue: 16})
	var accepted, ran atomic.Int64
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 100 {
				err := p.Submit(context.Background(), func(context.Context) error { ran.Add(1); return nil })
				//: accepted, or refused for good once closing began.
				if err == nil {
					accepted.Add(1)
				} else if errs.HasCode(err, pool.CodePoolClosed) {
					return
				}
			}
		})
	}
	time.Sleep(time.Millisecond)
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	wg.Wait()
	if accepted.Load() != ran.Load() {
		t.Errorf("accepted %d, ran %d", accepted.Load(), ran.Load())
	}
}

func TestDefaultsAndMisuse(t *testing.T) {
	t.Parallel()
	p := pool.NewPool(context.Background(), pool.Config{Workers: -3, Queue: -1})
	done := make(chan struct{})
	for p.Submit(t.Context(), func(context.Context) error { close(done); return nil }) != nil {
		runtime.Gosched()
	}
	<-done
	defer func() {
		if recover() == nil {
			t.Error("Submit of a nil task did not panic")
		}
		_ = p.Drain(context.Background())
	}()
	_ = p.Submit(t.Context(), nil)
}
