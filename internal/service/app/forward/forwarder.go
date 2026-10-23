package forward

import (
	"context"
	"sync"
	"time"

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
	// recount holds one request for Run to count the queue again; a request
	// already pending makes another one nothing.
	recount chan struct{}
	// reader lists the queue without leasing it; nil when the broker cannot,
	// and then the counts are this process's alone.
	reader corequeue.MessageReader
	// held is every batch counted, by identifier: its size. Pending is its
	// length and PendingBytes the sum of its sizes.
	held map[string]int64
	// leaving is every batch settled — sent, expired, dead-lettered or
	// dropped — that a count may still list, because the consumer settles a
	// batch before the broker forgets it: the count it was settled before,
	// by generation. A count forgets those it no longer lists. Nil when the
	// broker cannot list its messages.
	leaving map[string]uint64
	// recorded is every batch Enqueue kept while a count lists the queue,
	// nil outside one: the listing may have missed it.
	recorded map[string]struct{}
	// counted is when the queue was last counted, prompted when a batch
	// another process queued last asked for a count at once, generation how
	// many counts Run started.
	counted, prompted time.Time
	generation        uint64
	// cfg is the normalized configuration.
	cfg Config
	// stats is what the forwarder holds and has done.
	stats StatsValue
	// admit serialises Enqueue, so two producers never both make room for
	// one slot.
	admit sync.Mutex
	// publish is held from a publication until its batch is counted, so a
	// batch that leaves the queue before Enqueue counted it, and a count that
	// lists it first, wait for that and never take it for another
	// process's. It is taken before mu, never after.
	publish sync.Mutex
	// mu guards stats, held, leaving, recorded, counted, prompted and
	// generation.
	mu sync.Mutex
}

// New builds a forwarder over cfg.Broker. It starts nothing: Run sends. When
// the broker is a queue.MessageReader — every broker of the SDK is — New
// counts the backlog a previous process left, so the bounds hold across a
// restart, and Run counts it again once Config.RecountInterval has passed,
// so they hold against what another process changes in it; ctx bounds the
// first count.
func New(ctx context.Context, cfg Config) (*Forwarder, error) {
	//: refuse what could never forward.
	if err := cfg.validate(); err != nil {
		//: named by field.
		return nil, err
	}
	n := cfg.normalized()
	forwarder := &Forwarder{
		cfg: n, room: make(chan struct{}, 1), recount: make(chan struct{}, 1),
		held: make(map[string]int64), counted: n.Clock.Now(),
	}
	//: the backlog on disk, when the broker can list it without leasing it.
	if reader, ok := cfg.Broker.(corequeue.MessageReader); ok {
		forwarder.reader, forwarder.leaving = reader, make(map[string]uint64)
		listed, err := forwarder.list(ctx)
		//: a queue that cannot be read cannot be bounded.
		if err != nil {
			//: as the broker said it.
			return nil, err
		}
		//: every live message, queued or leased, is held.
		for id, batch := range listed {
			forwarder.hold(id, batch.size)
		}
	}
	//: ready; Run sends.
	return forwarder, nil
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
		f.settle("", &f.stats.Dropped)
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
	//: durable before it is counted kept.
	return f.publishCounted(ctx, batch)
}

// publishCounted publishes batch and counts it, under publish: a batch that
// leaves the queue before it is counted, and a count that lists it first,
// wait for it.
func (f *Forwarder) publishCounted(ctx context.Context, batch []byte) error {
	f.publish.Lock()
	defer f.publish.Unlock()
	message, err := f.cfg.Broker.Publish(ctx, batch)
	//: not kept.
	if err != nil {
		//: the broker's verdict, as it gave it.
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats.Enqueued++
	f.hold(message.ID, int64(len(batch)))
	//: a count listing the queue now may have missed it.
	if f.recorded != nil {
		f.recorded[message.ID] = struct{}{}
	}
	//: kept.
	return nil
}

// Stats returns the forwarder's counters, read at once. A read once
// Config.RecountInterval has passed since the queue was last counted asks
// Run to count it again, and returns the counts as they stand.
func (f *Forwarder) Stats() StatsValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dueRecount()
	//: a copy.
	return f.stats
}

// Run sends the queued batches until ctx ends, one consumer per
// Config.Parallelism, and returns what the queue's consumer returns. The
// batch being sent when ctx ends may finish within Config.DrainTimeout; one
// cut short is handed back uncounted and sent by the next process. Over a
// broker that lists its messages, Run also counts the queue again when
// asked: by a read of Stats or a batch leaving the queue once
// Config.RecountInterval has passed, and at once, once per interval at most,
// by the delivery of a batch this forwarder did not count.
//
// Goroutine lifecycle: one counts the queue when asked, until the consumer
// returns; Run waits for it before it returns.
func (f *Forwarder) Run(ctx context.Context) error {
	counting, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.recountLoop(counting)
	}()
	//: the queue's consumer, its lease renewed while a batch is retried.
	err := svcqueue.Consume(ctx, f.cfg.Broker, svcqueue.ConsumerConfig{
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
	stop()
	<-done
	//: what the consumer returned.
	return err
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
		f.settle(oldest.Message.ID, &f.stats.Dropped)
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
			f.settle("", &f.stats.Dropped)
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
	f.settle("", &f.stats.Dropped)
	//: the policy that refused it.
	return kerrs.Wrap(coreforward.ForwardFull, kerrs.WrapParams{},
		kerrs.String("policy", f.cfg.WhenFull.String()))
}

// hold counts the batch id of size, with mu held or before the forwarder is
// shared.
func (f *Forwarder) hold(id string, size int64) {
	f.held[id] = size
	f.stats.Pending = len(f.held)
	f.stats.PendingBytes += size
}

// release takes the counted batch id out of the counts, with mu held, and
// wakes a producer waiting for room.
func (f *Forwarder) release(id string) {
	f.stats.PendingBytes -= f.held[id]
	delete(f.held, id)
	f.stats.Pending = len(f.held)
	//: wake a producer waiting for room, without ever blocking.
	select {
	//: signalled.
	case f.room <- struct{}{}:
	//: a signal is already pending.
	default:
	}
}

// settle records the batch id leaving the queue under counter — "" for one
// that never entered it. A batch the forwarder does not count may be one
// whose publication it has not counted yet: settle waits for that
// publication. One it still does not count is another process's, which
// takes nothing from the counts, and Run counts the queue again at once —
// once per Config.RecountInterval at most, so a stream of batches another
// instance queued does not list the queue once per batch.
func (f *Forwarder) settle(id string, counter *uint64) {
	f.mu.Lock()
	*counter++
	_, held := f.held[id]
	//: never in the queue, or counted: settled under one lock.
	if id == "" || held {
		f.leave(id)
		f.mu.Unlock()
		//: settled.
		return
	}
	f.mu.Unlock()
	f.publish.Lock()
	defer f.publish.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	//: counted once its publication was.
	if _, held = f.held[id]; held {
		f.leave(id)
		//: settled.
		return
	}
	f.forget(id)
	now := f.cfg.Clock.Now()
	//: another process's: a count says what else it changed, at once once per interval.
	if now.Sub(f.prompted) >= f.cfg.RecountInterval {
		f.prompted = now
		f.askRecount()
		//: asked.
		return
	}
	f.dueRecount()
}

// leave takes the batch id, which left the queue, out of the counts, with mu
// held: nothing for "", which never entered it.
func (f *Forwarder) leave(id string) {
	//: a batch that never entered the queue.
	if id == "" {
		//: nothing held.
		return
	}
	f.release(id)
	f.forget(id)
	f.dueRecount()
}

// forget records that the batch id was settled, with mu held: the broker
// forgets it once the consumer acknowledges it, after the settle, and a count
// that lists it before must not count it again.
func (f *Forwarder) forget(id string) {
	//: a broker that cannot list keeps no record.
	if f.leaving != nil {
		f.leaving[id] = f.generation
	}
}
