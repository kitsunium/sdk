package commit_test

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/commit"
)

// onCallerStack reports whether the running flush was called from Do — on
// the caller's goroutine — rather than from a leading goroutine, whose stack
// starts at lead.
func onCallerStack() bool {
	buf := make([]byte, 1<<16)
	return strings.Contains(string(buf[:runtime.Stack(buf, false)]), "concur/commit.(*Group[...]).Do(")
}

// TestAnIdleDoFlushesOnTheCallersGoroutine pins the idle path (#813): a Do on
// an idle group whose ctx can never end flushes its item alone, on the
// caller's goroutine, with the caller's own context — and a Do whose ctx can
// end, or on a group with a gathering window, still leads a batch on a
// goroutine of its own, since its departure, or the followers it waits for,
// are what the batch is for.
func TestAnIdleDoFlushesOnTheCallersGoroutine(t *testing.T) {
	t.Parallel()
	type key struct{}
	type tc struct {
		name string
		// cancellable gives Do a context that can end.
		cancellable bool
		// maxWait is the group's gathering window, on a manual clock.
		maxWait time.Duration
		inline  bool
	}
	tests := []tc{
		{name: "a context that can never end", inline: true},
		{name: "a context that can end", cancellable: true},
		{name: "a gathering window", maxWait: time.Nanosecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ctx := context.WithValue(context.Background(), key{}, "v")
		if c.cancellable {
			var cancel context.CancelFunc
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
		}
		manual := clock.NewManualClock(time.Unix(0, 0))
		var onCaller bool
		var value any
		var items []int
		group := commit.New(func(flushCtx context.Context, batch []int) error {
			onCaller, value, items = onCallerStack(), flushCtx.Value(key{}), append([]int(nil), batch...)
			return nil
		}, commit.Config{MaxWait: c.maxWait, Clock: manual})
		defer group.Close()
		done := make(chan error, 1)
		go func() { done <- group.Do(ctx, 7) }()
		if c.maxWait > 0 {
			manual.BlockUntil(1)
			manual.Advance(time.Second)
		}
		if err := <-done; err != nil {
			t.Fatalf("Do = %v", err)
		}
		if onCaller != c.inline || value != "v" || len(items) != 1 || items[0] != 7 {
			t.Errorf("flushed %v on the caller's goroutine = %v with value %v, want [7], %v, v", items, onCaller, value, c.inline)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestInlineAndBatchedFlushesNeverOverlap runs the idle path against the
// batched one under load: flushes still run one at a time, every item is
// flushed exactly once, and every caller's flush began after it called.
func TestInlineAndBatchedFlushesNeverOverlap(t *testing.T) {
	t.Parallel()
	var running, overlaps, inline atomic.Int64
	var mu sync.Mutex
	flushed := map[int]int{}
	group := commit.New(func(_ context.Context, items []int) error {
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer running.Add(-1)
		if len(items) == 1 && onCallerStack() {
			inline.Add(1)
		}
		runtime.Gosched()
		mu.Lock()
		for _, item := range items {
			flushed[item]++
		}
		mu.Unlock()
		return nil
	}, commit.Config{})
	defer group.Close()
	const callers, rounds = 32, 50
	var wg sync.WaitGroup
	for c := range callers {
		wg.Go(func() {
			for r := range rounds {
				if err := group.Do(context.Background(), c*rounds+r); err != nil {
					t.Errorf("Do = %v", err)
				}
			}
		})
	}
	wg.Wait()
	if overlaps.Load() != 0 {
		t.Fatalf("%d flushes overlapped another", overlaps.Load())
	}
	for item := range callers * rounds {
		if flushed[item] != 1 {
			t.Fatalf("item %d flushed %d times, want once", item, flushed[item])
		}
	}
	t.Logf("%d of %d calls flushed inline", inline.Load(), callers*rounds)
}

// TestAGroupServesAgainAfterAnInlinePanic: the idle path's panic reaches its
// caller, the flush's only participant, and the group is idle again after
// it — neither left claimed nor closed.
func TestAGroupServesAgainAfterAnInlinePanic(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	group := commit.New(func(context.Context, []int) error {
		if calls.Add(1) == 1 {
			panic("boom")
		}
		return nil
	}, commit.Config{})
	defer group.Close()
	func() {
		defer func() {
			if got := recover(); got != "boom" {
				t.Fatalf("recovered %v, want the flush's panic", got)
			}
		}()
		_ = group.Do(context.Background(), 1)
	}()
	if err := group.Do(context.Background(), 2); err != nil || calls.Load() != 2 {
		t.Fatalf("Do after a panic = %v after %d flushes, want nil after 2", err, calls.Load())
	}
}
