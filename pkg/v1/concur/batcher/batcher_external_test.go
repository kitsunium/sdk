package batcher_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/concur/batcher"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// errDown is what a failing deliver function returns. A plain stdlib error is
// right here: a test fixture, wrapped by the batcher as a caller's would be.
var errDown = errors.New("down")

// recorder is a Sink that keeps every batch it is handed.
type recorder[T any] struct {
	mu      sync.Mutex
	batches [][]T
}

// deliver records batch; a Sink is never entered concurrently, the lock is for
// the test's reads.
func (r *recorder[T]) deliver(_ context.Context, batch []T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, slices.Clone(batch))
	return nil
}

// got returns the batches delivered so far.
func (r *recorder[T]) got() [][]T {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.batches)
}

// mustAdd adds item to b, failing the test on an error: no cap is reached in
// the tests that call it, so Add delivers nothing and has nothing to report.
func mustAdd(t *testing.T, b *batcher.Batcher[int], item int) {
	t.Helper()
	if err := b.Add(t.Context(), item); err != nil {
		t.Fatalf("Add(%d) = %v", item, err)
	}
}

// equalBatches compares two lists of batches.
func equalBatches[T comparable](a, b [][]T) bool {
	return slices.EqualFunc(a, b, func(x, y []T) bool { return slices.Equal(x, y) })
}

// TestAddDeliversAtACap asserts that Add delivers the pending batch on the
// caller's goroutine the moment it reaches the item cap or the weight cap,
// and not before.
func TestAddDeliversAtACap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cfg   batcher.Config[string]
		items []string
		want  [][]string
	}{
		{"three items at MaxItems 3", batcher.Config[string]{MaxItems: 3}, []string{"a", "b", "c", "d"}, [][]string{{"a", "b", "c"}}},
		{
			"a weight of five at MaxWeight 5",
			batcher.Config[string]{MaxWeight: 5, WeightOf: func(s string) int64 { return int64(len(s)) }},
			[]string{"ab", "cd", "e", "f"},
			[][]string{{"ab", "cd", "e"}},
		},
		{"no cap, nothing until a flush", batcher.Config[string]{}, []string{"a", "b"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := &recorder[string]{}
			b := batcher.NewBatcher(rec.deliver, tc.cfg)
			for _, item := range tc.items {
				if err := b.Add(t.Context(), item); err != nil {
					t.Fatalf("Add(%q) = %v", item, err)
				}
			}
			if got := rec.got(); !equalBatches(got, tc.want) {
				t.Errorf("delivered %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCloseDeliversThePendingBatchAndClosesTheBatcher asserts Flush and Close
// deliver what is pending, that a closed batcher refuses Add and Flush with
// BatcherClosed, and that a second Close is a no-op.
func TestCloseDeliversThePendingBatchAndClosesTheBatcher(t *testing.T) {
	t.Parallel()
	rec := &recorder[int]{}
	b := batcher.NewBatcher(rec.deliver, batcher.Config[int]{})
	ctx := t.Context()
	mustAdd(t, b, 1)
	if err := b.Flush(ctx); err != nil {
		t.Fatalf("Flush = %v", err)
	}
	mustAdd(t, b, 2)
	if err := b.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if got := rec.got(); !equalBatches(got, [][]int{{1}, {2}}) {
		t.Errorf("delivered %v, want [[1] [2]]", got)
	}
	if err := b.Add(ctx, 3); !errors.Is(err, batcher.BatcherClosed) {
		t.Errorf("Add after Close = %v, want BatcherClosed", err)
	}
	if err := b.Flush(ctx); !errors.Is(err, batcher.BatcherClosed) {
		t.Errorf("Flush after Close = %v, want BatcherClosed", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Errorf("a second Close = %v, want nil", err)
	}
}

// TestADeliveryFailureIsWrapped asserts that a deliver function's error comes
// back as BatcherDeliverFailed — by errors.Is and by code through pkg/v1/errs
// — with the original cause still reachable.
func TestADeliveryFailureIsWrapped(t *testing.T) {
	t.Parallel()
	b := batcher.NewBatcher(func(context.Context, []int) error { return errDown }, batcher.Config[int]{})
	mustAdd(t, b, 1)
	err := b.Flush(t.Context())
	if !errors.Is(err, batcher.BatcherDeliverFailed) || !errors.Is(err, errDown) {
		t.Errorf("Flush = %v, want BatcherDeliverFailed wrapping %v", err, errDown)
	}
	if code, ok := errs.CodeOf(batcher.BatcherDeliverFailed); !ok || !errs.HasCode(err, code) {
		t.Errorf("Flush = %v, does not carry BatcherDeliverFailed's code", err)
	}
}

// TestFlushEveryTicksOnTheInjectedClock asserts that the background delivery
// runs on Config.Clock: a pkg/v1 ManualClock advanced past FlushEvery delivers
// the pending batch with no wall-clock wait.
func TestFlushEveryTicksOnTheInjectedClock(t *testing.T) {
	t.Parallel()
	mc := clock.NewManualClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	delivered := make(chan []int, 1)
	b := batcher.NewBatcher(func(_ context.Context, batch []int) error {
		delivered <- slices.Clone(batch)
		return nil
	}, batcher.Config[int]{FlushEvery: time.Second, Clock: mc})
	mustAdd(t, b, 7)
	mc.BlockUntil(1) // the ticker is armed on the batcher's own goroutine
	mc.Advance(time.Second)
	select {
	case got := <-delivered:
		if !slices.Equal(got, []int{7}) {
			t.Errorf("the tick delivered %v, want [7]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("advancing the clock past FlushEvery delivered nothing")
	}
	if err := b.Close(t.Context()); err != nil {
		t.Errorf("Close = %v", err)
	}
}

// ExampleNewBatcher batches five items by three: one batch at the cap, the
// rest when the batcher closes.
func ExampleNewBatcher() {
	b := batcher.NewBatcher(func(_ context.Context, batch []int) error {
		fmt.Println(batch)
		return nil
	}, batcher.Config[int]{MaxItems: 3})
	ctx := context.Background()
	for i := 1; i <= 5; i++ {
		if err := b.Add(ctx, i); err != nil {
			fmt.Println(err)
		}
	}
	if err := b.Close(ctx); err != nil {
		fmt.Println(err)
	}
	// Output:
	// [1 2 3]
	// [4 5]
}
