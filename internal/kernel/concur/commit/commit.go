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
	// mu guards pending, tail, inlineDone, inline and closed.
	mu sync.Mutex
	// pending is the batch accepting items; nil when none is.
	pending *batch[T]
	// tail is the most recent batch's done, which the next batch flushes
	// after; nil before the first.
	tail <-chan struct{}
	// inlineDone is closed when the running inline flush returns; made only
	// when a batch or Close has to wait for it, so an inline flush nobody
	// waits on costs no channel.
	inlineDone chan struct{}
	// inline says whether a flush runs on its caller's goroutine (Do's idle
	// path), and whether it is still the most recent flush — no batch started
	// after it —, in which case the next batch waits on it, not on tail.
	inline inlineState
	// closed is set by Close.
	closed bool
}

// inlineState is where a Group stands with a flush run on its caller's
// goroutine.
type inlineState uint8

const (
	// inlineNone: no flush runs on a caller's goroutine.
	inlineNone inlineState = iota
	// inlineBehind: one runs, and a batch already waits on it.
	inlineBehind
	// inlineLatest: one runs, and it is the group's most recent flush.
	inlineLatest
)

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
// One case runs it on the caller's goroutine: a ctx that can never end (its
// Done is nil), on an idle group — no batch gathering, none flushing — with no
// MaxWait. The flush then carries this item alone, with ctx itself: nobody
// shares it and nobody can leave it, so a batch and its goroutine would buy
// nothing. Callers arriving meanwhile gather into the next batch, which
// flushes after it, so flushes still run one at a time, in order.
//
// Do returns [Closed] once Close has run. If the flush panics, every
// participant panics with the same value.
func (g *Group[T]) Do(ctx context.Context, item T) error {
	//: an idle group, a context that can never end and no gathering window:
	//: nobody to share the flush with and no departure to honour.
	if ctx.Done() == nil && g.cfg.MaxWait <= 0 && g.startInline() {
		//: the flush on this goroutine, alone, its verdict this caller's.
		return g.runInline(ctx, item)
	}
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

// startInline claims the group for a flush on the caller's goroutine, and
// reports whether it did: only when the group is open and idle — no batch
// gathering, none flushing, no other inline flush —, so the flush carries this
// caller's item alone and runs after every flush before it.
func (g *Group[T]) startInline() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: a closed group refuses through join; a busy one batches through it.
	if g.closed || g.inline != inlineNone || g.pending != nil || !settled(g.tail) {
		//: the batched path.
		return false
	}
	g.inline = inlineLatest
	//: claimed: a later Do batches after this flush.
	return true
}

// runInline flushes item on the caller's goroutine and releases the group
// whatever the flush does, a panic included, which reaches this caller — the
// flush's only participant.
func (g *Group[T]) runInline(ctx context.Context, item T) error {
	defer g.endInline()
	//: ctx can never end, so it carries its values and no cancellation, as
	//: the batched path's context.WithoutCancel does.
	return g.flush(ctx, []T{item})
}

// endInline releases the group after an inline flush and wakes what waits on
// it: the batch gathered meanwhile, or Close.
func (g *Group[T]) endInline() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inline = inlineNone
	//: a channel exists only when something waits for this flush.
	if g.inlineDone != nil {
		close(g.inlineDone)
		g.inlineDone = nil
	}
}

// last returns what the next batch, or Close, waits on: the inline flush
// while it is the most recent flush — its channel made on the first need —,
// else the most recent batch's done. g.mu is held.
func (g *Group[T]) last() <-chan struct{} {
	//: no inline flush, or a batch already chained after it.
	if g.inline != inlineLatest {
		//: nil before the first batch.
		return g.tail
	}
	//: the first waiter makes the channel endInline closes.
	if g.inlineDone == nil {
		g.inlineDone = make(chan struct{})
	}
	//: the running inline flush.
	return g.inlineDone
}

// settled reports whether done — a batch's, or nil before the first — is
// closed, i.e. whether no flush of it is still running.
func settled(done <-chan struct{}) bool {
	//: no batch yet.
	if done == nil {
		//: nothing runs.
		return true
	}
	select {
	//: its flush has returned.
	case <-done:
		//: settled.
		return true
	default:
		//: still flushing.
		return false
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
		current = newBatch[T](g.last())
		g.pending, g.tail = current, current.done
		//: this batch is now the most recent flush, after any inline one.
		if g.inline == inlineLatest {
			g.inline = inlineBehind
		}
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
// already started has been flushed — and a flush an idle Do runs on its
// own goroutine, which Close waits for even when no batch exists. It is
// idempotent.
func (g *Group[T]) Close() {
	g.mu.Lock()
	g.closed = true
	last := g.last()
	g.mu.Unlock()
	//: batches flush in order, so the last one's verdict means all are in.
	if last != nil {
		<-last
	}
}
