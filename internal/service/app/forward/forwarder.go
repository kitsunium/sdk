package forward

import (
	"context"
	"sync"

	coreforward "github.com/kitsunium/sdk/internal/core/app/forward"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// Forwarder holds batches in a durable queue and sends each one later,
// retried while its failure is retryable and its age allows, dead-lettered
// when the sender refuses it for good, and counted at every step. Enqueue
// keeps a batch; Run sends; Stats reads the counters. It is safe for
// concurrent use.
type Forwarder struct {
	// room holds one signal, sent when a batch leaves the queue, which the
	// one producer waiting under Block — Enqueue is serialised — wakes on. A
	// signal nobody waited for only makes the next wait look again.
	room chan struct{}
	// cfg is the normalized configuration.
	cfg Config
	// stats is what the forwarder holds and has done.
	stats StatsValue
	// admit serialises Enqueue, so two producers never both make room for
	// one slot.
	admit sync.Mutex
	// mu guards stats.
	mu sync.Mutex
}

// New builds a forwarder over cfg.Broker. It starts nothing: Run sends. When
// the broker is a queue.MessageReader — every broker of the SDK is — New
// counts the backlog a previous process left, so the bounds hold across a
// restart; ctx bounds that count.
func New(ctx context.Context, cfg Config) (*Forwarder, error) {
	//: refuse what could never forward.
	if err := cfg.validate(); err != nil {
		//: named by field.
		return nil, err
	}
	forwarder := &Forwarder{cfg: cfg.normalized(), room: make(chan struct{}, 1)}
	//: the backlog on disk, when the broker can list it without leasing it.
	if reader, ok := cfg.Broker.(corequeue.MessageReader); ok {
		//: every live message, queued or leased, is held.
		if err := forwarder.countBacklog(ctx, reader); err != nil {
			//: a queue that cannot be read cannot be bounded.
			return nil, err
		}
	}
	//: ready; Run sends.
	return forwarder, nil
}

// countBacklog adds every live message of reader to Pending and PendingBytes.
func (f *Forwarder) countBacklog(ctx context.Context, reader corequeue.MessageReader) error {
	after := ""
	//: page by page, in the broker's identifier order.
	for {
		page, err := reader.Messages(ctx, after, countPage)
		//: the medium failed.
		if err != nil {
			//: surfaced as the broker said it.
			return err
		}
		//: one message, one batch.
		for _, live := range page {
			f.stats.Pending++
			f.stats.PendingBytes += int64(len(live.Message.Payload))
			after = live.Message.ID
		}
		//: a short page is the last.
		if len(page) < countPage {
			//: counted.
			return nil
		}
	}
}

// Enqueue keeps batch in the queue, durably once it returns nil, for Run to
// send. An empty batch is kept nowhere. A batch larger than MaxBytes is
// refused with BatchTooLarge; one the queue has no room for is handled by
// Config.WhenFull — older batches dropped, or ForwardFull, or a bounded wait
// — and every batch lost to a bound is counted Dropped. ctx bounds the
// publication and the wait.
func (f *Forwarder) Enqueue(ctx context.Context, batch []byte) error {
	size := int64(len(batch))
	//: nothing to keep.
	if size == 0 {
		//: no publication.
		return nil
	}
	//: no policy can make room for it.
	if size > f.cfg.MaxBytes {
		f.settle(0, &f.stats.Dropped)
		//: refused with its size and the bound, never its bytes.
		return kerrs.Wrap(coreforward.BatchTooLarge, kerrs.WrapParams{},
			kerrs.Int64("bytes", size), kerrs.Int64("max_bytes", f.cfg.MaxBytes))
	}
	f.admit.Lock()
	defer f.admit.Unlock()
	//: the policy decides when it does not fit.
	if err := f.makeRoom(ctx, size); err != nil {
		//: refused, counted where it was decided.
		return err
	}
	f.reserve(size)
	//: durable before it is counted kept.
	if _, err := f.cfg.Broker.Publish(ctx, batch); err != nil {
		f.unreserve(size)
		//: the broker's verdict, as it gave it.
		return err
	}
	f.mu.Lock()
	f.stats.Enqueued++
	f.mu.Unlock()
	//: kept.
	return nil
}

// Stats returns the forwarder's counters, read at once.
func (f *Forwarder) Stats() StatsValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	//: a copy.
	return f.stats
}

// Run sends the queued batches until ctx ends, one consumer per
// Config.Parallelism, and returns what the queue's consumer returns. The
// batch being sent when ctx ends may finish within Config.DrainTimeout; one
// cut short is handed back uncounted and sent by the next process.
func (f *Forwarder) Run(ctx context.Context) error {
	//: the queue's consumer, its lease renewed while a batch is retried.
	return svcqueue.Consume(ctx, f.cfg.Broker, svcqueue.ConsumerConfig{
		Handler:       f.deliver,
		Clock:         f.cfg.Clock,
		PollInterval:  f.cfg.PollInterval,
		Parallelism:   f.cfg.Parallelism,
		BatchSize:     1,
		SettleTimeout: f.cfg.SettleTimeout,
		DrainTimeout:  f.cfg.DrainTimeout,
		AutoExtend:    true,
		// A batch sent again after a crash between the collector's
		// acceptance and the acknowledgement is a duplicate span or point,
		// which no forwarder can prevent — a delta metric point is then
		// counted twice, as the package documentation says (ADR 0185 D6).
		HandlerIsIdempotent: true,
	})
}

// makeRoom applies the full policy until size fits.
func (f *Forwarder) makeRoom(ctx context.Context, size int64) error {
	//: the policy in force.
	switch f.cfg.WhenFull {
	//: wait, bounded.
	case Block:
		//: the wait decides.
		return f.block(ctx, size)
	//: refuse the newest.
	case DropNewest:
		//: room, or refused.
		if f.fits(size) {
			//: it fits.
			return nil
		}
		//: refused and counted.
		return f.full()
	//: DropOldest, the zero.
	default:
		//: older batches out until it fits.
		return f.dropOldest(ctx, size)
	}
}

// dropOldest takes the oldest receivable batches out of the queue until size
// fits, each counted Dropped; when none can be taken the new batch is
// refused instead.
func (f *Forwarder) dropOldest(ctx context.Context, size int64) error {
	//: one batch out per round.
	for !f.fits(size) {
		deliveries, err := f.cfg.Broker.Receive(ctx, 1)
		//: the medium failed.
		if err != nil {
			//: as the broker said it.
			return err
		}
		//: the only batches left are being sent or waiting.
		if len(deliveries) == 0 {
			//: the newest is the one lost.
			return f.full()
		}
		oldest := deliveries[0]
		//: acknowledged is removed.
		if ackErr := f.cfg.Broker.Ack(ctx, oldest.Lease.Receipt); ackErr != nil {
			//: as the broker said it.
			return ackErr
		}
		f.settle(int64(len(oldest.Message.Payload)), &f.stats.Dropped)
	}
	//: it fits.
	return nil
}

// block waits for room up to BlockTimeout on the clock, then refuses.
func (f *Forwarder) block(ctx context.Context, size int64) error {
	//: room already: no timer armed.
	if f.fits(size) {
		//: it fits.
		return nil
	}
	timer := f.cfg.Clock.NewTimer(f.cfg.BlockTimeout)
	defer timer.Stop()
	//: until it fits or the wait ends.
	for !f.fits(size) {
		//: a batch leaving, the bound, or the caller.
		select {
		//: something left the queue: look again.
		case <-f.room:
		//: the wait is over.
		case <-timer.C():
			//: refused and counted.
			return f.full()
		//: the caller gave up.
		case <-ctx.Done():
			f.settle(0, &f.stats.Dropped)
			//: the caller's reason.
			return ctx.Err()
		}
	}
	//: it fits.
	return nil
}

// fits reports whether a batch of size fits under both bounds.
func (f *Forwarder) fits(size int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	//: under the lock.
	return f.holds(size)
}

// holds is fits with mu held.
func (f *Forwarder) holds(size int64) bool {
	//: both bounds.
	return f.stats.Pending < f.cfg.MaxBatches && f.stats.PendingBytes+size <= f.cfg.MaxBytes
}

// full counts the new batch Dropped and refuses it with ForwardFull.
func (f *Forwarder) full() error {
	f.settle(0, &f.stats.Dropped)
	//: the policy that refused it.
	return kerrs.Wrap(coreforward.ForwardFull, kerrs.WrapParams{},
		kerrs.String("policy", f.cfg.WhenFull.String()))
}

// reserve counts a batch held before it is published, so a consumer that
// acknowledges it at once never takes the count below zero.
func (f *Forwarder) reserve(size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats.Pending++
	f.stats.PendingBytes += size
}

// unreserve takes back a reservation whose publication failed.
func (f *Forwarder) unreserve(size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leave(size)
}

// settle records a batch of size leaving the queue under counter — size 0
// for one that never entered it — and wakes a producer waiting for room.
func (f *Forwarder) settle(size int64, counter *uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*counter++
	//: a batch that was held.
	if size > 0 {
		f.leave(size)
	}
}

// leave takes a held batch of size out of the counts, never below zero — a
// batch another process queued is not counted — and wakes a waiting
// producer.
func (f *Forwarder) leave(size int64) {
	f.stats.Pending = max(f.stats.Pending-1, 0)
	f.stats.PendingBytes = max(f.stats.PendingBytes-size, 0)
	//: wake a producer waiting for room, without ever blocking.
	select {
	//: signalled.
	case f.room <- struct{}{}:
	//: a signal is already pending.
	default:
	}
}
