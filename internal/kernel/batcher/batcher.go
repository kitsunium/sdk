// Package batcher provides a generic coalescing buffer: items are appended via
// Add and handed to a deliver closure in batches — flushed eagerly when an
// item-count or weight cap is reached, on an optional background ticker, or on
// an explicit Flush / Close.
//
// The primitive is stdlib-only and domain-neutral (ADR 0014). It owns its own
// time.Ticker directly and does not depend on any other kernel lifecycle
// primitive. The deliver closure carries every domain-specific concern: a
// pre-delivery reorder, a per-batch key, or a byte-vs-count weight all live in
// the caller's Sink and WeightOf, never in the batcher.
//
// Concurrency: Add, Flush and Close are safe to call from multiple goroutines;
// a mutex guards the pending batch. The batch is swapped out under the lock and
// the deliver closure runs outside it, so a slow delivery never blocks a
// producer. Add returns batcher.Closed once Close has run.
package batcher

import (
	"context"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Sink delivers one coalesced batch. A non-nil error fails the batch: it is
// returned to the caller of Flush / Close and routed to Config.OnError on the
// background ticker path.
type Sink[T any] func(ctx context.Context, batch []T) error

// Batcher coalesces Add'd items into batches delivered through its Sink.
// It owns its own flush ticker and guards the pending batch with a mutex, so
// Add / Flush / Close are safe under concurrent callers (ADR 0014).
type Batcher[T any] struct {
	// deliver is the batch delivery seam supplied at construction.
	deliver Sink[T]
	// cfg holds the caps, weight function, interval and error hook.
	cfg Config[T]

	// mu guards buf + weight + closed against Add, Flush, Close and the ticker.
	mu     sync.Mutex
	buf    []T
	weight int64
	closed bool

	// stop/done drive the optional FlushEvery ticker goroutine; the Once
	// guards make the channel closes safe under concurrent Close calls.
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	doneOnce sync.Once
	ticking  bool
}

// NewBatcher builds a Batcher over deliver with cfg. When cfg.FlushEvery > 0 it
// spawns a ticker goroutine that flushes on the interval; the goroutine is
// joined by Close. A nil cfg.OnError is degraded to a no-op so the flush paths
// stay branch-free.
func NewBatcher[T any](deliver Sink[T], cfg Config[T]) *Batcher[T] {
	//: degrade a nil error hook to a no-op so background flush stays branch-free.
	if cfg.OnError == nil {
		//: discard ticker-flush errors when the caller wired no observer.
		cfg.OnError = func(error) {}
	}
	b := &Batcher[T]{
		deliver: deliver,
		cfg:     cfg,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	//: only run the ticker when a positive interval was requested.
	if cfg.FlushEvery > 0 {
		//: mark ticking so Close knows to join the goroutine.
		b.ticking = true
		//: background flusher bounds how long a partial batch waits.
		go b.loop(cfg.FlushEvery)
	}
	//: hand back the constructed batcher.
	return b
}

// Add appends item to the pending batch and eagerly flushes when the batch
// reaches MaxItems or MaxWeight. It returns batcher.Closed if Close has run; a
// cap-triggered deliver failure is returned wrapped as DeliverFailed.
func (b *Batcher[T]) Add(ctx context.Context, item T) error {
	//: append under the lock; the ticker / Flush share the pending batch.
	b.mu.Lock()
	//: reject Add after Close so no item is silently dropped.
	if b.closed {
		b.mu.Unlock()
		//: surface the closed-batcher sentinel to the caller.
		return BatcherClosed
	}
	b.buf = append(b.buf, item)
	b.weight += b.weigh(item)
	//: decide whether this append crossed an eager-flush threshold.
	full := b.full()
	b.mu.Unlock()
	//: flush eagerly when a cap is hit so memory stays bounded.
	if full {
		//: propagate a cap-flush failure to the Add caller.
		return b.flushOnce(ctx)
	}
	//: under cap — the item waits for the next flush.
	return nil
}

// Flush delivers the pending batch synchronously, returning any deliver error.
// It returns batcher.Closed if Close has run.
func (b *Batcher[T]) Flush(ctx context.Context) error {
	//: reject Flush after Close so the contract matches Add.
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	//: a closed batcher has nothing more to deliver.
	if closed {
		//: surface the closed-batcher sentinel to the caller.
		return BatcherClosed
	}
	//: caller-facing flush propagates the error rather than routing to OnError.
	return b.flushOnce(ctx)
}

// Close stops the ticker (if any), delivers the final batch under a background
// context, and marks the batcher closed. It is idempotent: a second Close
// returns nil after the first has drained the buffer.
func (b *Batcher[T]) Close(ctx context.Context) error {
	//: join the ticker goroutine first so it cannot race the final flush.
	if b.ticking {
		//: idempotent stop signal so a double Close never panics.
		b.stopOnce.Do(func() { close(b.stop) })
		//: wait for the loop to acknowledge exit.
		<-b.done
	}
	//: flip closed and capture the final batch atomically under the lock so a
	//: concurrent Add either lands in this batch or is rejected — never stranded.
	b.mu.Lock()
	b.closed = true
	batch := b.buf
	b.buf = nil
	b.weight = 0
	b.mu.Unlock()
	//: nothing buffered — closed-flag flip is the only work to do.
	if len(batch) == 0 {
		//: empty-batch fast path (also the idempotent second-Close path).
		return nil
	}
	//: deliver the captured final batch outside the lock.
	return b.deliverBatch(ctx, batch)
}

// flushOnce swaps out the pending batch under the lock and delivers it outside
// the lock. An empty batch is a no-op; a deliver error is wrapped as
// DeliverFailed with the cause reachable via errors.Is.
func (b *Batcher[T]) flushOnce(ctx context.Context) error {
	//: take ownership of the pending batch under the lock.
	b.mu.Lock()
	//: nothing buffered — release and report success.
	if len(b.buf) == 0 {
		b.mu.Unlock()
		//: empty-batch fast path.
		return nil
	}
	//: hand the batch off and reset; a nil buf starts a fresh backing array.
	batch := b.buf
	b.buf = nil
	b.weight = 0
	b.mu.Unlock()
	//: deliver outside the lock so a slow Sink never blocks producers.
	return b.deliverBatch(ctx, batch)
}

// deliverBatch runs the Sink on batch outside any lock and wraps a non-nil
// result as the typed DeliverFailed sentinel (the cause stays reachable via
// errors.Is). It is the shared delivery tail of flushOnce and Close.
func (b *Batcher[T]) deliverBatch(ctx context.Context, batch []T) error {
	//: run the Sink; a slow delivery never holds b.mu.
	if derr := b.deliver(ctx, batch); derr != nil {
		//: wrap the cause as the typed DeliverFailed sentinel (errors.Is reaches it).
		return errs.Wrap(derr, errs.WrapParams{
			Code:    CodeBatcherDeliverFailed,
			Reason:  "BATCHER_DELIVER_FAILED",
			Public:  "Batcher delivery failed",
			Private: "internal/kernel/batcher: the deliver closure returned an error",
		})
	}
	//: happy path — batch delivered.
	return nil
}

// loop runs the FlushEvery ticker until Close signals stop.
func (b *Batcher[T]) loop(every time.Duration) {
	//: signal Close that the goroutine has fully exited (guarded for safety).
	defer b.doneOnce.Do(func() { close(b.done) })
	//: periodic flusher bounds how long a partial batch waits.
	t := time.NewTicker(every)
	defer t.Stop()
	//: drain on every tick; exit promptly on stop.
	for {
		select {
		//: interval elapsed — flush whatever has accumulated.
		case <-t.C:
			//: route a tick-flush failure to the observer.
			if ferr := b.flushOnce(context.Background()); ferr != nil {
				//: surface the background failure via the configured hook.
				b.cfg.OnError(ferr)
			}
		//: Close requested — let Close perform the final flush.
		case <-b.stop:
			//: terminal exit; deferred close(done) unblocks Close.
			return
		}
	}
}

// weigh reports item's contribution to the batch weight: WeightOf when set,
// else 1 for count-only batching.
func (b *Batcher[T]) weigh(item T) int64 {
	//: a nil WeightOf means count-only — every item weighs one slot.
	if b.cfg.WeightOf == nil {
		//: count-only weight.
		return 1
	}
	//: caller-supplied weight (e.g. byte size).
	return b.cfg.WeightOf(item)
}

// full reports whether the pending batch has reached an eager-flush cap: the
// item-count cap (MaxItems>0) OR the weight cap (WeightOf set and MaxWeight>0).
// The caller holds b.mu.
func (b *Batcher[T]) full() bool {
	//: the item-count cap fires when MaxItems is positive and reached.
	countCap := b.cfg.MaxItems > 0 && len(b.buf) >= b.cfg.MaxItems
	//: the weight cap fires only when a WeightOf and positive MaxWeight exist.
	weightCap := b.cfg.WeightOf != nil && b.cfg.MaxWeight > 0 && b.weight >= b.cfg.MaxWeight
	//: either cap crossing forces an eager flush.
	return countCap || weightCap
}
