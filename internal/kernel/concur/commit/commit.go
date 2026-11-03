package commit

import (
	"context"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// Group coalesces concurrent [Group.Do] calls into one flush: every caller
// contributes an item, one goroutine runs the flush over all of them, and
// every caller receives that flush's verdict. It is group commit — N callers
// that each need their item durable share one fsync, where each would have
// paid its own.
//
// Safe for concurrent use. The zero value is NOT usable — construct with
// [New].
type Group[T any] struct {
	flush func(ctx context.Context, items []T) error
	cfg   Config
	clk   clock.Waiter
	// mu guards pending, tail and closed.
	mu sync.Mutex
	// pending is the batch accepting items; nil when none is.
	pending *batch[T]
	// tail is the most recent batch's done, which the next batch flushes
	// after; nil before the first.
	tail <-chan struct{}
	// closed is set by Close.
	closed bool
}

// New builds a Group that hands each batch of items to flush. A nil flush
// panics, at the call that made the mistake: there is nothing a Group could
// do with an item without one.
func New[T any](flush func(ctx context.Context, items []T) error, cfg Config) *Group[T] {
	//: refuse here, where the bug is, rather than at the first Do.
	if flush == nil {
		//: no default exists: what a flush does is the caller's whole purpose.
		panic("kernel/concur/commit: New requires a flush function")
	}
	clk := cfg.Clock
	//: nil is the production default, as everywhere a clock is injected.
	if clk == nil {
		clk = clock.System
	}
	//: no batch until the first Do.
	return &Group[T]{flush: flush, cfg: cfg, clk: clk}
}

// Do adds item to the batch being gathered — starting one if none is — and
// returns the verdict of the flush that carried it: every participant of a
// flush receives the same error, nil included.
//
// A caller whose ctx ends before the flush returns ctx.Err() at once, and its
// item is STILL flushed: leaving is a departure, not a withdrawal, since the
// flush may already be writing it. The flush runs on a goroutine of its own,
// with the first participant's context values and none of its cancellation.
//
// Do returns [Closed] once Close has run. If the flush panics, every
// participant panics with the same value.
func (g *Group[T]) Do(ctx context.Context, item T) error {
	joined, refusal := g.join(ctx, item)
	//: a closed Group takes no item.
	if refusal != nil {
		//: CommitClosed.
		return refusal
	}
	select {
	//: the verdict of the flush that carried the item.
	case <-joined.done:
		//: a panic in the flush is every participant's.
		if joined.panicked != nil {
			panic(joined.panicked)
		}
		//: the shared verdict.
		return joined.err
	//: this caller leaves; its item stays in the batch.
	case <-ctx.Done():
		//: a verdict already there is delivered rather than lost on a coin flip.
		select {
		case <-joined.done:
			//: the panic, as above.
			if joined.panicked != nil {
				panic(joined.panicked)
			}
			//: the shared verdict, despite the cancellation.
			return joined.err
		default:
		}
		//: the caller's own cancellation.
		return ctx.Err()
	}
}

// join appends item to the pending batch, starting one — and the goroutine
// that will flush it — when none is pending.
func (g *Group[T]) join(ctx context.Context, item T) (*batch[T], error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a closed Group takes no item.
	if g.closed {
		//: the sentinel.
		return nil, Closed
	}
	current := g.pending
	//: the first caller since the last seal leads a new batch.
	if current == nil {
		current = newBatch[T](g.tail)
		g.pending, g.tail = current, current.done
		go g.lead(context.WithoutCancel(ctx), current)
	}
	current.items = append(current.items, item)
	//: a full batch is sealed now: the next caller starts another.
	if g.cfg.MaxItems > 0 && len(current.items) >= g.cfg.MaxItems {
		current.markFull()
		g.pending = nil
	}
	//: the batch this caller waits on.
	return current, nil
}

// lead gathers the batch for MaxWait or until it is full, waits for the flush
// before it, seals it and flushes it, then publishes the verdict.
func (g *Group[T]) lead(ctx context.Context, current *batch[T]) {
	//: published whatever happens, a panic included.
	defer current.settle()
	defer func() { current.panicked = recover() }()
	//: the gathering window, on the injected clock.
	if g.cfg.MaxWait > 0 {
		timer := g.clk.NewTimer(g.cfg.MaxWait)
		select {
		case <-timer.C():
		case <-current.full:
			timer.Stop()
		}
	}
	//: one flush at a time, in the order the batches were started; the batch
	//: keeps gathering while the one before it flushes.
	if current.after != nil {
		<-current.after
	}
	items := g.seal(current)
	current.err = g.flush(ctx, items)
}

// seal stops current from taking items and returns them.
func (g *Group[T]) seal(current *batch[T]) []T {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a full batch was sealed by the Do that filled it.
	if g.pending == current {
		g.pending = nil
	}
	//: no Do appends to a sealed batch, so the slice is the flush's.
	return current.items
}

// Close refuses every later Do with [Closed] and returns once every batch
// already started has been flushed. It is idempotent.
func (g *Group[T]) Close() {
	g.mu.Lock()
	g.closed = true
	last := g.tail
	g.mu.Unlock()
	//: batches flush in order, so the last one's verdict means all are in.
	if last != nil {
		<-last
	}
}
