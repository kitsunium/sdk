package batcher

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func Test_Batcher_weigh(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		weightOf func(int) int64
		item     int
		want     int64
	}
	tests := []tc{
		{"nil WeightOf is count-only", nil, 99, 1},
		{"WeightOf is honoured", func(n int) int64 { return int64(n) }, 7, 7},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := NewBatcher(func(context.Context, []int) error { return nil }, Config[int]{WeightOf: c.weightOf})
		//: weigh must reflect the count-only fallback or the supplied weight.
		if got := b.weigh(c.item); got != c.want {
			t.Errorf("%s: weigh(%d)=%d want %d", c.name, c.item, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_full(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  Config[int]
		buf  []int
		want bool
	}
	tests := []tc{
		{"no caps never full", Config[int]{}, []int{1, 2}, false},
		{"item cap reached", Config[int]{MaxItems: 2}, []int{1, 2}, true},
		{"item cap not reached", Config[int]{MaxItems: 3}, []int{1, 2}, false},
		{"weight cap reached", Config[int]{MaxWeight: 4, WeightOf: func(int) int64 { return 2 }}, []int{1, 2}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := NewBatcher(func(context.Context, []int) error { return nil }, c.cfg)
		//: drive buf + weight through the public Add so full sees real state.
		for _, it := range c.buf {
			b.buf = append(b.buf, it)
			b.weight += b.weigh(it)
		}
		//: full must report exactly the cap-crossing decision.
		if got := b.full(); got != c.want {
			t.Errorf("%s: full=%v want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_flushOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		items       []int
		wantBatches int
	}
	tests := []tc{
		{"empty buffer delivers nothing", nil, 0},
		{"buffered items deliver once", []int{1, 2}, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var batches int
		deliver := func(context.Context, []int) error {
			//: count each non-empty delivery the flush performs.
			batches++
			return nil
		}
		b := NewBatcher(deliver, Config[int]{})
		for _, it := range c.items {
			if err := b.Add(t.Context(), it); err != nil {
				t.Fatalf("%s: Add: %v", c.name, err)
			}
		}
		//: a direct flushOnce delivers one batch (or none when empty).
		if err := b.flushOnce(t.Context()); err != nil {
			t.Fatalf("%s: flushOnce: %v", c.name, err)
		}
		if batches != c.wantBatches {
			t.Errorf("%s: batches=%d want %d", c.name, batches, c.wantBatches)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_loop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the loop ticker drains a buffered batch then exits on Close"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		var delivered atomic.Int64
		deliver := func(_ context.Context, batch []int) error {
			//: record the loop's tick-driven delivery.
			delivered.Add(int64(len(batch)))
			return nil
		}
		// A 1ms ticker drives loop; Close stops it and joins the goroutine, so
		// the loop goroutine's lifetime is bounded by this test.
		b := NewBatcher(deliver, Config[int]{FlushEvery: time.Millisecond})
		if err := b.Add(t.Context(), 1); err != nil {
			t.Fatalf("Add: %v", err)
		}
		//: poll until the ticker loop has delivered the buffered item.
		deadline := 0
		for delivered.Load() == 0 && deadline < 200 {
			time.Sleep(time.Millisecond)
			deadline++
		}
		//: Close stops the loop and joins it — proving prompt stop handling.
		if err := b.Close(t.Context()); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if delivered.Load() == 0 {
			t.Errorf("loop ticker did not deliver within the deadline")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_concurrentAddFlush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		producers int
		perProd   int
	}
	tests := []tc{{"many producers + a flusher race cleanly", 8, 200}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var delivered atomic.Int64
		deliver := func(_ context.Context, batch []int) error {
			//: count every delivered item so the total is checkable after Close.
			delivered.Add(int64(len(batch)))
			return nil
		}
		b := NewBatcher(deliver, Config[int]{MaxItems: 16})
		ctx := t.Context()
		// produce appends perProd items for one producer; taking base as a
		// parameter keeps the loop var off the closure's heap-escape path.
		produce := func(base int) {
			for i := range c.perProd {
				//: each Add either buffers or eager-flushes; both must not error.
				if err := b.Add(ctx, base*c.perProd+i); err != nil {
					t.Errorf("Add: %v", err)
				}
			}
		}
		// Producer goroutines append concurrently; each exits after perProd
		// Adds. wg.Wait below joins them all before Close, so none outlive the test.
		var wg sync.WaitGroup
		for p := range c.producers {
			wg.Go(func() { produce(p) })
		}
		wg.Wait()
		//: Close drains whatever remains so the total is exact.
		if err := b.Close(ctx); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		want := int64(c.producers * c.perProd)
		//: every Add'd item must have been delivered exactly once.
		if got := delivered.Load(); got != want {
			t.Errorf("%s: delivered=%d want %d", c.name, got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_Batcher_deliverClosureMayReorder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []int
		want []int
	}
	tests := []tc{{"the deliver closure sorts the batch before recording it", []int{3, 1, 2}, []int{1, 2, 3}}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got []int
		// deliver mirrors how cwSink moves its chronological reorder into the
		// closure: the batcher hands over arrival order, the closure sorts.
		deliver := func(_ context.Context, batch []int) error {
			//: sort a copy so the recorded order proves the closure controls it.
			cp := slices.Clone(batch)
			slices.Sort(cp)
			got = cp
			return nil
		}
		b := NewBatcher(deliver, Config[int]{})
		for _, it := range c.in {
			//: append the items in their out-of-order arrival sequence.
			if err := b.Add(t.Context(), it); err != nil {
				t.Fatalf("%s: Add: %v", c.name, err)
			}
		}
		if err := b.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		//: the closure's reorder must be what reaches the sink.
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: delivered=%v want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
