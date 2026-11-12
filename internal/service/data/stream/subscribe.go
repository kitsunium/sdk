package stream

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/concur/group"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// Subscribe reads s on behalf of the consumer group cfg.Group until ctx
// ends: one worker per partition it is assigned, each handing cfg.Handler
// the partition's records in offset order, batch after batch, and moving
// the group's offset past each batch the handler accepted. It is
// internal/service/data/queue.Consume for a stream: the handler runs on a
// goroutine Subscribe owns, a handler's error is never returned, and every
// worker has returned before Subscribe does.
//
// A worker starts at the offset the group committed on its partition, or
// at the partition's first retained offset when the group never committed
// there; an offset retention has since removed resumes at the first one
// still kept, the records in between never delivered. It takes the
// stream's [corestream.Waker] signal before each Read, so an Append in this
// process wakes it at once, and waits at most cfg.PollInterval on cfg.Clock
// for what another process appended.
//
// # A failing handler blocks its partition, by design
//
// A batch whose handler fails is handed to it again after the cfg.Retry
// wait, and the partition goes no further until it is accepted: the records
// of one key are delivered in the order they were appended, which is the
// promise a stream makes and a queue does not (ADR 0054's deferred per-key
// ordering, kept here as head-of-line blocking stated rather than hidden).
// The other partitions carry on. With cfg.Park, a batch failing After times
// is narrowed to its first record, and a record failing After times alone
// is published to the park's queue as a dead letter and the group's offset
// committed past it.
//
// # What a stop or a crash leaves
//
// A stop cancels the handler's context. A batch accepted as the stop lands
// is still committed, on ctx without its cancellation, bounded by
// cfg.SettleTimeout on cfg.Clock, as a dead letter is published and a fence
// released. Under [CommitAfterHandler] a crash between the handler's return
// and the commit delivers the batch again; under [CommitByHandler] on the
// SQL engine, the commit being part of the handler's own transaction, there
// is no such moment.
//
// # Fencing
//
// With cfg.Fence each worker holds the lock
// "stream:<Name>:<Group>:<partition>" while it reads the partition, so two
// processes running the same group over the same partitions never read one
// partition at once: a worker whose lock is held elsewhere waits a
// PollInterval and asks again. A lease that can expire is renewed a third
// of its life after it was taken or last renewed; a renewal refused gives
// the partition up — its batch is not committed — and the worker goes back
// to asking for the lock, resuming at whatever the group committed.
//
// It returns nil when ctx ends, [corestream.SubscriberMisconfigured] for a
// configuration it cannot run, and the storage's error — the stream's, the
// park's queue's, the fence's — when one of them refuses, which stops every
// worker: a medium that has stopped answering is not one a subscriber can
// poll its way out of.
func Subscribe(ctx context.Context, s corestream.Stream, cfg SubscriberConfig) error {
	committer, ok := s.(corestream.Committer)
	//: a group's offset is kept by the stream; a stream that keeps none
	//: cannot be subscribed to.
	if !ok {
		//: SubscriberMisconfigured, naming the stream.
		return subscriberMisconfigured("Stream")
	}
	//: refused at the wiring site, before any worker starts.
	if invalid := cfg.validate(s.Partitions()); invalid != nil {
		//: SubscriberMisconfigured, naming the field.
		return invalid
	}
	sub := &subscriber{stream: s, committer: committer, cfg: cfg.normalized(s.Partitions())}
	sub.waker, _ = s.(corestream.Waker)
	//: joined: each worker's storage failure is its own fact, and the first
	//: cancels the others, which read the same medium.
	workers, _ := group.NewJoined(ctx, len(sub.cfg.Partitions))
	//: one worker per partition, the only reader of it in this process.
	for _, partition := range sub.cfg.Partitions {
		workers.Go(func(stop context.Context) error {
			//: nil for a stopped worker, the medium's error otherwise.
			return sub.run(stop, partition)
		})
	}
	//: every worker has returned.
	return workers.Wait()
}

// subscriber is what every worker of one Subscribe shares; nothing in it
// changes after Subscribe built it.
type subscriber struct {
	stream    corestream.Stream
	committer corestream.Committer
	// waker is the stream's, nil when it has none.
	waker corestream.Waker
	cfg   SubscriberConfig
}

// cursor is one worker's position on its partition.
type cursor struct {
	// fence is the lease the worker reads under, nil without a Fence.
	fence *heldFence
	// retryAt is when a refused batch may be delivered again.
	retryAt time.Time
	// narrowUntil is the offset below which batches are one record: the
	// end of a batch the park narrowed.
	narrowUntil corestream.Offset
	partition   int
	// next is the offset read next.
	next corestream.Offset
	// failures counts the consecutive refusals of the batch at next.
	failures int
}

// run is one partition's worker: hold its fence, follow the partition, and
// ask again for a fence lost.
func (w *subscriber) run(ctx context.Context, partition int) error {
	//: until stopped, or until the medium refuses.
	for ctx.Err() == nil {
		fence, held, err := w.acquire(ctx, partition)
		//: the locker could not answer.
		if err != nil {
			//: nil for a stop, the locker's error otherwise.
			return ignoreStop(ctx, err)
		}
		//: held elsewhere: stand by a poll interval and ask again.
		if !held {
			w.sleep(ctx, nil, w.cfg.PollInterval)
			continue
		}
		err = w.follow(ctx, &cursor{partition: partition, fence: fence})
		w.release(ctx, fence)
		//: a storage failure stops every worker.
		if err != nil {
			//: nil for a stop, the medium's error otherwise.
			return ignoreStop(ctx, err)
		}
	}
	//: stopped.
	return nil
}

// follow reads the partition from the group's offset until ctx ends, the
// fence is lost (nil), or the medium refuses (its error).
func (w *subscriber) follow(ctx context.Context, c *cursor) error {
	start, err := w.start(ctx, c.partition)
	//: the cursor or the bounds could not be read.
	if err != nil {
		//: the medium's error.
		return err
	}
	c.next = start
	//: one turn after another, until stopped or lost.
	for ctx.Err() == nil {
		//: a lease due for renewal is renewed; a refused one gives up.
		if !w.renew(ctx, c.fence) {
			//: the partition is another process's now.
			return nil
		}
		//: a refused batch waits out its retry.
		if wait := c.retryAt.Sub(w.cfg.Clock.Now()); wait > 0 {
			w.sleep(ctx, nil, w.bounded(wait, c.fence))
			continue
		}
		//: one Read, one handler call, one outcome.
		if err := w.turn(ctx, c); err != nil {
			//: the medium's error.
			return err
		}
	}
	//: stopped.
	return nil
}

// start returns the offset the group reads first on partition: the one it
// committed, or the first retained when it never committed.
func (w *subscriber) start(ctx context.Context, partition int) (corestream.Offset, error) {
	next, err := w.committer.Committed(ctx, w.cfg.Group, partition)
	//: a group new to this partition starts at the beginning of what is kept.
	if errs.HasCode(err, corestream.CodeUnknownGroup) {
		bounds, boundsErr := w.stream.Bounds(ctx, partition)
		//: the first retained offset, or the medium's error.
		return bounds.First, boundsErr
	}
	//: the committed offset, or the medium's error.
	return next, err
}

// turn reads the batch at the cursor and hands it to the handler, or waits
// when the partition holds nothing past it.
func (w *subscriber) turn(ctx context.Context, c *cursor) error {
	var signal <-chan struct{}
	//: taken BEFORE the Read, so an Append landing between an empty Read and
	//: the wait closes it and is not missed.
	if w.waker != nil {
		signal = w.waker.Wake()
	}
	entries, err := w.stream.Read(ctx, c.partition, c.next, w.limit(c))
	//: retention removed the cursor's records: resume at the first kept.
	if errs.HasCode(err, corestream.CodeOffsetTrimmed) {
		bounds, boundsErr := w.stream.Bounds(ctx, c.partition)
		c.next, c.failures = bounds.First, 0
		//: the bounds' failure, or the next turn.
		return boundsErr
	}
	//: the medium refused.
	if err != nil {
		//: the medium's error.
		return err
	}
	//: nothing past the cursor: wait for an Append or the poll.
	if len(entries) == 0 {
		w.sleep(ctx, signal, w.bounded(w.cfg.PollInterval, c.fence))
		//: the next turn.
		return nil
	}
	//: the handler's verdict decides the next offset.
	return w.settle(ctx, c, entries)
}

// limit is how many records the next Read asks for: one while the cursor is
// inside a batch the park narrowed.
func (w *subscriber) limit(c *cursor) int {
	//: narrowed to find the record that keeps failing.
	if c.next < c.narrowUntil {
		//: one record.
		return 1
	}
	//: the configured batch.
	return w.cfg.BatchSize
}

// settle runs the handler over entries and acts on its verdict: the offset
// moves past an accepted batch, a refused one is retried, narrowed or
// parked.
func (w *subscriber) settle(ctx context.Context, c *cursor, entries []corestream.EntryValue) error {
	past := entries[len(entries)-1].Offset + 1
	//: accepted: the offset moves past the batch.
	if w.handle(ctx, entries) {
		c.failures, c.retryAt = 0, time.Time{}
		//: the handler committed itself.
		if w.cfg.Commit == CommitByHandler {
			c.next = past
			return nil
		}
		//: the subscriber commits; a lease lost meanwhile leaves the batch
		//: to the new holder.
		return w.advance(ctx, c, past)
	}
	//: a stop's refusal is the stop, not a failure to count.
	if ctx.Err() != nil {
		return nil
	}
	c.failures++
	//: the park's turn once the batch was refused After times.
	if w.cfg.Park != nil && c.failures >= w.cfg.Park.After {
		//: the batch, or the one record it was narrowed to.
		return w.narrowOrPark(ctx, c, entries)
	}
	c.retryAt = w.cfg.Clock.Now().Add(w.cfg.Retry.Delay(c.failures))
	//: retried after the wait.
	return nil
}

// handle runs the handler, a panic read as a refusal, and reports whether it
// accepted the batch.
func (w *subscriber) handle(ctx context.Context, entries []corestream.EntryValue) (accepted bool) {
	//: a panicking handler refuses its batch, as an error would, and the
	//: partition is not left behind by a goroutine nobody recovers.
	defer func() {
		//: the panic is the refusal.
		if recover() != nil {
			accepted = false
		}
	}()
	//: nil accepts.
	return w.cfg.Handler(ctx, entries) == nil
}

// advance commits past as the group's next offset on the cursor's partition
// and moves the cursor there; a fence that cannot be renewed first leaves
// both where they are.
func (w *subscriber) advance(ctx context.Context, c *cursor, past corestream.Offset) error {
	//: never commit under a lease another process may hold.
	if !w.renew(ctx, c.fence) {
		//: the next turn sees the lease lost.
		return nil
	}
	err := w.settled(ctx, func(settleCtx context.Context) error {
		//: the group's offset.
		return w.committer.Commit(settleCtx, w.cfg.Group, c.partition, past)
	})
	//: not committed: the batch is delivered again, after a restart.
	if err != nil {
		//: the medium's error.
		return err
	}
	c.next = past
	//: committed.
	return nil
}

// sleep waits for the first of ctx's end, signal and d on the clock.
func (w *subscriber) sleep(ctx context.Context, signal <-chan struct{}, d time.Duration) {
	timer := w.cfg.Clock.NewTimer(max(d, 0))
	defer timer.Stop()
	//: the first of the three.
	select {
	//: stopped.
	case <-ctx.Done():
	//: an Append in this process.
	case <-signal:
	//: the wait elapsed.
	case <-timer.C():
	}
}

// settled runs call on ctx without its cancellation, bounded by
// SettleTimeout on the clock from the instant ctx ends. It starts a
// background goroutine, bound, that ends when settled returns (done is
// closed on every path) or once it has cancelled the call, so none outlives
// the call it bounds.
func (w *subscriber) settled(ctx context.Context, call func(context.Context) error) error {
	settleCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go w.bound(ctx, done, cancel)
	//: the call's own answer, or the cancellation the bound sent it.
	return call(settleCtx)
}

// bound cancels a settling call SettleTimeout after ctx ends, unless it is
// done first.
func (w *subscriber) bound(ctx context.Context, done <-chan struct{}, cancel context.CancelFunc) {
	//: the call's end, or the stop that starts the bound.
	select {
	//: finished while the subscriber ran: no bound.
	case <-done:
		return
	//: the stop starts the bound.
	case <-ctx.Done():
	}
	timer := w.cfg.Clock.NewTimer(w.cfg.SettleTimeout)
	defer timer.Stop()
	//: the call's end, or the bound's.
	select {
	//: finished inside the bound.
	case <-done:
	//: the medium did not answer in time.
	case <-timer.C():
		cancel()
	}
}

// ignoreStop reads a failure that is the subscriber's own stop as a stop.
func ignoreStop(ctx context.Context, cause error) error {
	//: the caller stopped; the medium only said so.
	if ctx.Err() != nil {
		return nil
	}
	//: the medium genuinely refused.
	return cause
}
