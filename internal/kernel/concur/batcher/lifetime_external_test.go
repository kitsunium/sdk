package batcher_test

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/batcher"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// waitGoroutines polls until the process runs at most want goroutines, or
// fails: the goleak-style count #1040's acceptance names, without the module.
func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	for range 500 {
		//: the goroutine ends on its own schedule after the cancellation.
		if runtime.NumGoroutine() <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines = %d, want at most %d", runtime.NumGoroutine(), want)
}

// TestCloseBoundsATickFlushStuckInTheSink is #1040's acceptance: a tick flush
// whose Sink waits for its context, and a Close whose context ends while the
// ticker is still inside that Sink. Close returns BatcherCloseTimedOut once
// its context has ended — it does not wait for the Sink —, the Sink sees its
// context cancelled, and the ticker goroutine exits. Not parallel: it counts
// the process's goroutines.
func TestCloseBoundsATickFlushStuckInTheSink(t *testing.T) {
	before := runtime.NumGoroutine()
	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	entered := make(chan struct{})
	observed := make(chan error, 1)
	sink := func(ctx context.Context, _ []int) error {
		close(entered)
		<-ctx.Done()
		observed <- ctx.Err()
		return ctx.Err()
	}
	b := batcher.NewBatcher(sink, batcher.Config[int]{FlushEvery: time.Second, Clock: mc})
	if err := b.Add(t.Context(), 1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mc.BlockUntil(1)
	mc.Advance(time.Second)
	<-entered
	//: the caller's deadline, on no wall clock: it ends while the Sink holds the ticker.
	ctx, cancel := context.WithCancel(context.Background())
	closed := make(chan error, 1)
	go func() { closed <- b.Close(ctx) }()
	cancel()
	select {
	case err := <-closed:
		if !errs.HasCode(err, batcher.CodeBatcherCloseTimedOut) || !errors.Is(err, context.Canceled) {
			t.Errorf("Close = %v, want BatcherCloseTimedOut wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after its context ended: the join of the tick flush is unbounded")
	}
	select {
	case err := <-observed:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("the Sink's context ended with %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the tick flush's context was never cancelled")
	}
	waitGoroutines(t, before)
}

// TestTickFlushContextEndsWithClose: the context a tick flush hands the Sink
// is the batcher's, cancelled once Close has returned — a Sink that kept it
// sees it done.
func TestTickFlushContextEndsWithClose(t *testing.T) {
	t.Parallel()
	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	got := make(chan context.Context, 1)
	sink := func(ctx context.Context, _ []int) error {
		got <- ctx
		return nil
	}
	b := batcher.NewBatcher(sink, batcher.Config[int]{FlushEvery: time.Second, Clock: mc})
	if err := b.Add(t.Context(), 1); err != nil {
		t.Fatalf("Add: %v", err)
	}
	mc.BlockUntil(1)
	mc.Advance(time.Second)
	tickCtx := <-got
	if tickCtx.Err() != nil {
		t.Fatalf("the tick's context ended before Close: %v", tickCtx.Err())
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if tickCtx.Err() == nil {
		t.Error("the tick's context is still live after Close returned")
	}
}

// TestCloseBoundsTheFinalBatchBehindACallersFlush: a FlushCaller Add holds
// the Sink, an item is pending, and Close's context ends while the final
// batch waits for the Sink. Close returns BatcherCloseTimedOut instead of
// queueing behind the stuck delivery.
func TestCloseBoundsTheFinalBatchBehindACallersFlush(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	sink := func(_ context.Context, batch []int) error {
		//: the capped batch holds the Sink until the test releases it.
		if batch[0] == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	b := batcher.NewBatcher(sink, batcher.Config[int]{MaxItems: 2})
	addDone := make(chan error, 1)
	go func() {
		_ = b.Add(context.Background(), 1)
		addDone <- b.Add(context.Background(), 2)
	}()
	<-entered
	if err := b.Add(t.Context(), 3); err != nil {
		t.Fatalf("Add of the pending item: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Close(ctx); !errs.HasCode(err, batcher.CodeBatcherCloseTimedOut) {
		t.Errorf("Close behind a stuck delivery = %v, want BatcherCloseTimedOut", err)
	}
	close(release)
	if err := <-addDone; err != nil {
		t.Errorf("the capped Add = %v", err)
	}
}

// TestBackgroundAddNeverRunsTheSink is #817's: under FlushBackground a capped
// batch is delivered by the batcher's goroutine — the producer never enters
// the Sink — and Close delivers every item added, exactly once, in order.
func TestBackgroundAddNeverRunsTheSink(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []int
	producer := make(chan struct{})
	var onProducer atomic.Int64
	sink := func(_ context.Context, batch []int) error {
		//: a delivery on the producer's goroutine would find this channel open.
		select {
		case <-producer:
		default:
			onProducer.Add(1)
		}
		mu.Lock()
		got = append(got, batch...)
		mu.Unlock()
		time.Sleep(100 * time.Microsecond)
		return nil
	}
	b := batcher.NewBatcher(sink, batcher.Config[int]{MaxItems: 4, FlushOn: batcher.FlushBackground})
	close(producer)
	const n = 1000
	for i := range n {
		if err := b.Add(t.Context(), i); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(got, slices.Collect(func(yield func(int) bool) {
		for i := range n {
			if !yield(i) {
				return
			}
		}
	})) {
		t.Errorf("delivered %d items, not every added item once in order", len(got))
	}
	if onProducer.Load() != 0 {
		t.Errorf("%d deliveries ran before the producer started", onProducer.Load())
	}
}

// TestBackgroundConcurrentProducersLoseNothing: many producers under
// FlushBackground racing a slow Sink and Close; every accepted item is
// delivered exactly once.
func TestBackgroundConcurrentProducersLoseNothing(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	seen := map[int]int{}
	sink := func(_ context.Context, batch []int) error {
		mu.Lock()
		for _, v := range batch {
			seen[v]++
		}
		mu.Unlock()
		time.Sleep(50 * time.Microsecond)
		return nil
	}
	b := batcher.NewBatcher(sink, batcher.Config[int]{MaxItems: 8, FlushOn: batcher.FlushBackground, FlushEvery: time.Millisecond})
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for p := range 8 {
		wg.Go(func() {
			for i := range 500 {
				if b.Add(t.Context(), p*1000+i) == nil {
					accepted.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if err := b.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if int64(len(seen)) != accepted.Load() {
		t.Errorf("delivered %d distinct items, accepted %d", len(seen), accepted.Load())
	}
	for v, c := range seen {
		if c != 1 {
			t.Errorf("item %d delivered %d times", v, c)
		}
	}
}

// TestBackgroundFailureGoesToOnError: a background delivery has no caller,
// so its failure reaches OnError, wrapped as BatcherDeliverFailed.
func TestBackgroundFailureGoesToOnError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	failed := make(chan error, 1)
	b := batcher.NewBatcher(func(context.Context, []int) error { return boom }, batcher.Config[int]{
		MaxItems: 1, FlushOn: batcher.FlushBackground,
		OnError: func(err error) {
			select {
			case failed <- err:
			default:
			}
		},
	})
	if err := b.Add(t.Context(), 1); err != nil {
		t.Fatalf("Add under FlushBackground = %v, want nil", err)
	}
	select {
	case err := <-failed:
		if !errs.HasCode(err, batcher.CodeBatcherDeliverFailed) || !errors.Is(err, boom) {
			t.Errorf("OnError got %v, want BatcherDeliverFailed wrapping boom", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the background failure never reached OnError")
	}
	if err := b.Close(t.Context()); err != nil {
		t.Errorf("Close: %v", err)
	}
}
