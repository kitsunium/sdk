package batcher_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/batcher"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// recorder captures delivered batches so the tests need no real sink.
type recorder struct {
	mu      sync.Mutex
	batches [][]int
	err     error
}

func (r *recorder) deliver(_ context.Context, batch []int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	//: a configured error simulates a delivery failure.
	if r.err != nil {
		return r.err
	}
	//: snapshot the batch so later reuse cannot alias a recorded delivery.
	r.batches = append(r.batches, slices.Clone(batch))
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.batches)
}

func Test_NewBatcher(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		onError func(error)
	}
	tests := []tc{
		{"nil onError is degraded to a no-op", nil},
		{"explicit onError is kept", func(error) {}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &recorder{}
		b := batcher.NewBatcher(r.deliver, batcher.Config[int]{OnError: c.onError})
		//: the constructor must always yield a usable, non-nil batcher.
		if b == nil {
			t.Fatalf("%s: NewBatcher returned nil", c.name)
		}
		//: a fresh batcher must accept an Add without error.
		if err := b.Add(t.Context(), 1); err != nil {
			t.Errorf("%s: Add on fresh batcher: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		items       []int
		wantBatches int
		wantBody    []int
	}
	tests := []tc{
		{"three items coalesce into one batch", []int{1, 2, 3}, 1, []int{1, 2, 3}},
		{"empty buffer flush is a no-op", nil, 0, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &recorder{}
		b := batcher.NewBatcher(r.deliver, batcher.Config[int]{})
		for _, it := range c.items {
			//: an Add below the (absent) cap must never error.
			if err := b.Add(t.Context(), it); err != nil {
				t.Fatalf("%s: Add(%d): %v", c.name, it, err)
			}
		}
		//: an explicit flush delivers exactly one coalesced batch (or none).
		if err := b.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		if r.count() != c.wantBatches {
			t.Fatalf("%s: batches=%d want %d", c.name, r.count(), c.wantBatches)
		}
		//: when a body was expected, it must equal the appended items in order.
		if c.wantBatches == 1 && !slices.Equal(r.batches[0], c.wantBody) {
			t.Errorf("%s: body=%v want %v", c.name, r.batches[0], c.wantBody)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_Add(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		cfg        batcher.Config[int]
		adds       int
		wantMinBat int
	}
	tests := []tc{
		{"MaxItems forces a flush", batcher.Config[int]{MaxItems: 2}, 2, 1},
		{"MaxWeight forces a flush", batcher.Config[int]{MaxWeight: 4, WeightOf: func(int) int64 { return 2 }}, 2, 1},
		{"below caps defers", batcher.Config[int]{MaxItems: 100}, 1, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &recorder{}
		b := batcher.NewBatcher(r.deliver, c.cfg)
		for range c.adds {
			//: each Add returns nil on the happy path even when it triggers a flush.
			if err := b.Add(t.Context(), 1); err != nil {
				t.Fatalf("%s: Add: %v", c.name, err)
			}
		}
		//: the cap-trigger arm flushes inline; the under-cap arm defers.
		if r.count() < c.wantMinBat {
			t.Errorf("%s: batches=%d want >= %d", c.name, r.count(), c.wantMinBat)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_AddDeliverError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Flush wraps the deliver error as DeliverFailed"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("deliver boom")
		r := &recorder{err: boom}
		b := batcher.NewBatcher(r.deliver, batcher.Config[int]{})
		//: buffer one item so the flush actually calls deliver.
		if err := b.Add(t.Context(), 1); err != nil {
			t.Fatalf("Add: %v", err)
		}
		err := b.Flush(t.Context())
		//: errors.Is must reach the original cause through the wrap.
		if !errors.Is(err, boom) {
			t.Errorf("Flush err=%v want cause %v", err, boom)
		}
		//: errors.Is must also reach the typed DeliverFailed sentinel.
		if !errors.Is(err, batcher.BatcherDeliverFailed) {
			t.Errorf("Flush err=%v does not match BatcherDeliverFailed", err)
		}
		//: HasCode confirms the dotted-quad routing code is attached.
		if !errs.HasCode(err, batcher.CodeBatcherDeliverFailed) {
			t.Errorf("Flush err=%v missing CodeBatcherDeliverFailed", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		flushEvery time.Duration
	}
	tests := []tc{
		{"close flushes the final batch (no ticker)", 0},
		{"close stops the ticker and flushes", 5 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := &recorder{}
		b := batcher.NewBatcher(r.deliver, batcher.Config[int]{FlushEvery: c.flushEvery})
		//: buffer one item so Close has something to deliver.
		if err := b.Add(t.Context(), 1); err != nil {
			t.Fatalf("%s: Add: %v", c.name, err)
		}
		//: Close must deliver the buffered item and join any ticker.
		if err := b.Close(t.Context()); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		if r.count() == 0 {
			t.Errorf("%s: Close did not flush the final batch", c.name)
		}
		//: Add after Close must report the Closed sentinel, not silently drop.
		if err := b.Add(t.Context(), 2); !errors.Is(err, batcher.BatcherClosed) {
			t.Errorf("%s: Add after Close=%v want BatcherClosed", c.name, err)
		}
		//: a second Close is idempotent.
		if err := b.Close(t.Context()); err != nil {
			t.Errorf("%s: second Close: %v", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_CloseTicker(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the ticker flushes a buffered batch without an explicit Flush"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		r := &recorder{}
		b := batcher.NewBatcher(r.deliver, batcher.Config[int]{FlushEvery: 2 * time.Millisecond})
		t.Cleanup(func() {
			//: surface a close failure rather than discarding it.
			if err := b.Close(t.Context()); err != nil {
				t.Errorf("cleanup close: %v", err)
			}
		})
		//: buffer one item and let the background ticker deliver it.
		if err := b.Add(t.Context(), 1); err != nil {
			t.Fatalf("Add: %v", err)
		}
		//: poll until the background ticker has delivered the batch.
		deadline := 0
		for r.count() == 0 && deadline < 200 {
			time.Sleep(2 * time.Millisecond)
			deadline++
		}
		if r.count() == 0 {
			t.Errorf("ticker did not flush within the deadline")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
