package commit_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/commit"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recorder is a flush that records every batch it was handed.
type recorder struct {
	mu      sync.Mutex
	batches [][]int
	err     error
	gate    chan struct{}
}

func (r *recorder) flush(_ context.Context, items []int) error {
	if r.gate != nil {
		<-r.gate
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, slices.Clone(items))
	return r.err
}

func (r *recorder) flushed() [][]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

// TestEveryParticipantReceivesTheFlushVerdict is #803's first acceptance: the
// flush's error reaches every participant of that flush. MaxItems seals the
// batch at eight, so the hour of MaxWait never has to pass.
func TestEveryParticipantReceivesTheFlushVerdict(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	failure := errors.New("disk full")
	rec := &recorder{err: failure}
	group := commit.New(rec.flush, commit.Config{MaxItems: 8, MaxWait: time.Hour, Clock: manual})
	results := make(chan error, 8)
	for i := range 8 {
		go func() { results <- group.Do(context.Background(), i) }()
	}
	for range 8 {
		if err := <-results; !errors.Is(err, failure) {
			t.Fatalf("a participant got %v, want the flush's error", err)
		}
	}
	if batches := rec.flushed(); len(batches) != 1 || len(batches[0]) != 8 {
		t.Fatalf("flushes = %v, want one batch of 8", batches)
	}
}

// TestADepartedParticipantsItemIsStillFlushed is #803's second acceptance: a
// participant whose ctx ends before the flush returns ctx.Err(), and its item
// is in the flush all the same.
func TestADepartedParticipantsItemIsStillFlushed(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	rec := &recorder{}
	group := commit.New(rec.flush, commit.Config{MaxItems: 2, MaxWait: time.Hour, Clock: manual})
	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() { left <- group.Do(ctx, 1) }()
	//: the leader's gathering timer is armed: item 1 is in the batch.
	manual.BlockUntil(1)
	cancel()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("the departed participant got %v, want context.Canceled", err)
	}
	//: the second item fills the batch, which is flushed without the hour.
	if err := group.Do(context.Background(), 2); err != nil {
		t.Fatalf("the remaining participant got %v", err)
	}
	if batches := rec.flushed(); len(batches) != 1 || !slices.Equal(batches[0], []int{1, 2}) {
		t.Fatalf("flushes = %v, want [[1 2]]: the departed item must still be flushed", batches)
	}
}

// TestMaxItemsFlushesWithoutWaitingOutMaxWait seals a full batch at once.
func TestMaxItemsFlushesWithoutWaitingOutMaxWait(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(0, 0))
	rec := &recorder{}
	group := commit.New(rec.flush, commit.Config{MaxItems: 3, MaxWait: time.Hour, Clock: manual})
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			if err := group.Do(context.Background(), i); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if batches := rec.flushed(); len(batches) != 1 || len(batches[0]) != 3 {
		t.Fatalf("flushes = %v, want one batch of 3 flushed without the hour", batches)
	}
}

// TestCallersArrivingDuringAFlushShareTheNext is the group commit itself with
// no wait: while one flush runs, every caller that arrives joins one batch,
// flushed next — two flushes for seventeen callers. In a synctest bubble, so
// "every caller has joined" is synctest.Wait, not a sleep.
func TestCallersArrivingDuringAFlushShareTheNext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		rec := &recorder{gate: gate}
		group := commit.New(rec.flush, commit.Config{})
		var wg sync.WaitGroup
		wg.Go(func() { _ = group.Do(context.Background(), 0) })
		//: the first flush is running, held at the gate.
		synctest.Wait()
		for i := 1; i <= 16; i++ {
			wg.Go(func() { _ = group.Do(context.Background(), i) })
		}
		//: all sixteen have joined the next batch, whose flush waits on the first.
		synctest.Wait()
		close(gate)
		wg.Wait()
		batches := rec.flushed()
		if len(batches) != 2 || len(batches[0]) != 1 || len(batches[1]) != 16 {
			t.Fatalf("flushes = %v, want [first] then the 16 that arrived during it", batches)
		}
	})
}

// TestFlushesRunOneAtATimeInOrder pins the ordering: no two flushes overlap,
// and a batch never flushes before the one started before it.
func TestFlushesRunOneAtATimeInOrder(t *testing.T) {
	t.Parallel()
	var running, overlaps atomic.Int64
	var order []int
	var mu sync.Mutex
	group := commit.New(func(_ context.Context, items []int) error {
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer running.Add(-1)
		mu.Lock()
		order = append(order, items...)
		mu.Unlock()
		return nil
	}, commit.Config{MaxItems: 1})
	for i := range 50 {
		if err := group.Do(context.Background(), i); err != nil {
			t.Fatal(err)
		}
	}
	if overlaps.Load() != 0 {
		t.Fatal("two flushes overlapped")
	}
	if !slices.IsSorted(order) {
		t.Fatalf("flush order %v is not the order of the calls", order)
	}
}

// TestDoAfterCloseIsRefused pins the sentinel, and that Close waits for the
// batches already started.
func TestDoAfterCloseIsRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gate := make(chan struct{})
		rec := &recorder{gate: gate}
		group := commit.New(rec.flush, commit.Config{})
		done := make(chan error, 1)
		go func() { done <- group.Do(context.Background(), 1) }()
		synctest.Wait()
		closed := make(chan struct{})
		go func() { group.Close(); close(closed) }()
		synctest.Wait()
		select {
		case <-closed:
			t.Fatal("Close returned before the batch it found was flushed")
		default:
		}
		close(gate)
		<-closed
		if err := <-done; err != nil {
			t.Fatalf("the batch before Close got %v", err)
		}
		err := group.Do(context.Background(), 2)
		if !errors.Is(err, commit.Closed) || !errs.HasCode(err, commit.CodeCommitClosed) {
			t.Fatalf("Do after Close = %v, want Closed", err)
		}
		group.Close()
	})
}

// TestAPanickingFlushPanicsEveryParticipant: the flush's panic is not lost on
// its goroutine; each participant re-raises it.
func TestAPanickingFlushPanicsEveryParticipant(t *testing.T) {
	t.Parallel()
	group := commit.New(func(context.Context, []int) error { panic("boom") }, commit.Config{})
	defer func() {
		if got := recover(); got != "boom" {
			t.Fatalf("recovered %v, want the flush's panic", got)
		}
	}()
	_ = group.Do(context.Background(), 1)
	t.Fatal("Do returned after a panicking flush")
}

// TestNewPanicsOnANilFlush mirrors the kernel's refusal of a missing function.
func TestNewPanicsOnANilFlush(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil) did not panic")
		}
	}()
	commit.New[int](nil, commit.Config{})
}
