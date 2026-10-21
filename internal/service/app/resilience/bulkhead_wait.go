package resilience

import (
	"context"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// waitingBulkhead is the bulkhead that queues: slots bounds the operations
// running, queue the calls waiting for one, and maxWait how long each waits on
// clk. Both semaphores are buffered channels, so a call that finds a slot free
// pays what the reject-mode bulkhead pays.
type waitingBulkhead struct {
	slots   chan struct{}
	queue   chan struct{}
	clk     clock.Timed
	maxWait time.Duration
}

// NewBulkheadWait returns a Runner that admits at most cfg.Max simultaneous
// operations (clamped to >= 1) and lets up to cfg.Queue more calls wait, each
// at most cfg.MaxWait on cfg.Clock, for a slot to free — Polly's
// ConcurrencyLimiter(permitLimit, queueLimit), failsafe-go's bulkhead
// WithMaxWaitTime. A call that finds the queue full, or whose wait runs out,
// returns BulkheadFull; one whose context ends while it waits returns
// ctx.Err() and frees its place in the queue.
//
// A non-positive Queue or MaxWait is no waiting at all: the call is
// rejected at once, as NewBulkhead rejects it, so the zero
// BulkheadWaitConfig is NewBulkhead(1). Waiters take the freed slots in
// the order they started waiting, as far as the Go runtime's channel
// queues keep it.
//
// A call that finds a slot free pays what NewBulkhead's does and
// allocates nothing; a call that waits arms one timer on cfg.Clock.
func NewBulkheadWait(cfg BulkheadWaitConfig) coreres.Runner {
	//: a bulkhead that cannot wait is the reject-mode one, slot for slot.
	if cfg.Queue <= 0 || cfg.MaxWait <= 0 {
		//: the zero configuration is NewBulkhead(1).
		return NewBulkhead(cfg.Max)
	}
	//: the queue and the wait, out of line so NewBulkheadWait stays small.
	return newWaitingBulkhead(cfg)
}

// newWaitingBulkhead builds the bulkhead that queues from a cfg whose Queue and
// MaxWait are both positive.
func newWaitingBulkhead(cfg BulkheadWaitConfig) *waitingBulkhead {
	//: at least one slot, as NewBulkhead clamps.
	limit := max(cfg.Max, 1)
	//: the queue and the slots are each a semaphore of their own size.
	return &waitingBulkhead{
		slots:   make(chan struct{}, limit),
		queue:   make(chan struct{}, cfg.Queue),
		clk:     timedOrSystem(cfg.Clock),
		maxWait: cfg.MaxWait,
	}
}

// Run admits op at once if a slot is free, else queues it for at most maxWait,
// else rejects with BulkheadFull.
func (b *waitingBulkhead) Run(ctx context.Context, op coreres.Operation) error {
	//: try to claim a slot without blocking — the hot path, unchanged.
	select {
	case b.slots <- struct{}{}:
		//: run under the held slot.
		return b.runHeld(ctx, op)
	default:
	}
	//: no slot: take a place in the queue, or be rejected at once.
	select {
	case b.queue <- struct{}{}:
	default:
		//: the queue is full too — reject fast.
		return wrapAs(coreres.BulkheadFull, nil)
	}
	//: wait for a slot, holding the place in the queue until the wait ends.
	if err := b.await(ctx); err != nil {
		//: the wait ran out, or the caller left.
		return err
	}
	//: run under the slot the wait claimed.
	return b.runHeld(ctx, op)
}

// runHeld runs op under a slot already claimed and releases it on return,
// panic included.
func (b *waitingBulkhead) runHeld(ctx context.Context, op coreres.Operation) error {
	//: release the slot once the operation returns.
	defer func() { <-b.slots }()
	//: run under the held slot.
	return op(ctx)
}

// await waits at most maxWait on the clock for a slot, the queue place already
// held, and leaves the queue whatever happens. nil means a slot was claimed.
func (b *waitingBulkhead) await(ctx context.Context) error {
	//: the queue place is given back when the wait ends, claimed or not.
	defer func() { <-b.queue }()
	timer := b.clk.NewTimer(b.maxWait)
	//: always release the timer.
	defer timer.Stop()
	select {
	//: a slot freed: it is this call's.
	case b.slots <- struct{}{}:
		//: claimed.
		return nil
	//: no slot freed in time.
	case <-timer.C():
		//: the wait ran out: rejected as a full bulkhead.
		return wrapAs(coreres.BulkheadFull, nil)
	//: the caller left first.
	case <-ctx.Done():
		//: its own verdict, not a rejection.
		return ctx.Err()
	}
}
