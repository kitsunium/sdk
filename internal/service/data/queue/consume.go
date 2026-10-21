package queue

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/group"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// armTimerRounds bounds how many times armUntil re-arms a timer the clock
// moved past while it was being armed.
const armTimerRounds int = 4

// Consume runs cfg.Handler against broker until ctx is done.
//
// It is the third column of ADR 0053's frontier made concrete: the handler
// runs on a goroutine this function owns, not on the publisher's, and it runs
// in a different process from the publisher whenever the broker is a durable
// one. Publish returned long ago and knows nothing about any of this.
//
// It returns nil when ctx ends — a cancelled consumer is a stopped consumer,
// not a failed one — and returns the broker's error when the BROKER fails,
// because a queue whose storage has stopped answering is not something a
// consumer can poll its way out of. A HANDLER's error is never returned: it
// is nacked, retried, and eventually dead-lettered, which is the entire point
// of the domain.
//
// Every worker returns before Consume does, so a caller that cancels and
// waits knows no handler is still running.
//
// # A stop hands back what it did not do (kitsunium/sdk#474)
//
// When ctx ends, a worker leases nothing more. The message its handler is
// running gets [ConsumerConfig.DrainTimeout] to finish before the handler's
// context is cancelled; what finishes is acknowledged, and what the stop cut
// short is handed back through the broker's [corequeue.Releaser] — receivable
// at once, its delivery not counted — as is every message of the batch the
// handler never started. A broker without the sibling is nacked instead. Each
// of those outcomes is sent past the stop, on ctx without its cancellation,
// bounded by [ConsumerConfig.SettleTimeout] on the consumer's clock, so a
// caller's stop budget must cover DrainTimeout plus SettleTimeout.
//
// # A handler says when to try again (ADR 0183)
//
// A failure the handler marked with [corequeue.RetryAfter] is handed back
// through the broker's [corequeue.DelayedNacker] with the delay it names,
// its delivery counted as a nack counts it; a broker without the sibling is
// nacked, and the message waits the policy's retry delay instead.
// [corequeue.DoNotRetry] wins over it, and a failure a stop cut short is
// handed back uncounted, as any other is.
//
// # A handler ends before its lease does
//
// The handler's context is cancelled a margin before the lease it runs under
// ends — a tenth of the lease, at most SettleTimeout, read on the consumer's
// clock, which must be the broker's — with [corequeue.LeaseExpired] as its
// context.Cause: past that instant the broker may hand the message to
// another consumer. A message of a batch whose lease is already within that
// margin when its turn comes is never started; it is handed back like the
// batch a stop interrupts. With [ConsumerConfig.AutoExtend] the lease is
// renewed instead, through the broker's [corequeue.LeaseExtender].
//
// A renewal the handler makes itself is not one Consume sees: the bound
// stays the lease the handler was handed, and the outcome is reported on
// that lease's receipt, which the renewal voided. So a handler that
// extends its own lease returns right after — parking its message, as
// the mail spool does, which the void receipt's LEASE_EXPIRED leaves
// parked — and a handler that must run longer than its lease sets
// AutoExtend and does not extend it itself.
func Consume(ctx context.Context, broker corequeue.Broker, cfg ConsumerConfig) error {
	//: the assertion the SDK cannot check, refused at the wiring site.
	if invalid := cfg.validate(); invalid != nil {
		//: ConsumerMisconfigured, naming the field.
		return invalid
	}
	engine := newConsumer(broker, cfg.normalized())
	//: a worker that fails for a STORAGE reason takes the others down with
	//: it, because they are all polling the same broken medium — the group
	//: cancels its context on the first failure. It is a JOINED group because
	//: each worker's failure is its own fact about that medium, not an echo
	//: of the first: every one reaches the caller, in worker order.
	workers, _ := group.NewJoined(ctx, engine.cfg.Parallelism)
	//: one independent pull loop per worker; they coordinate only through the
	//: broker, which is the thing that already arbitrates.
	for range engine.cfg.Parallelism {
		workers.Go(func(stop context.Context) error {
			//: nil for a stopped worker, the broker's error for a failed one.
			return engine.pump(stop)
		})
	}
	//: nil unless a worker hit the storage; a cancelled consumer is a stopped
	//: one, not a failed one. A worker's panic outside the handler — the
	//: handler's own is recovered into a nack — is re-raised here, after its
	//: siblings have stopped, rather than ending the process from a goroutine
	//: nobody can recover.
	return workers.Wait()
}

// consumer is what every worker of one Consume call shares: the broker, the
// resolved configuration and the broker's siblings, asked once. Nothing in it
// changes after newConsumer, so the workers read it without a lock.
type consumer struct {
	// broker is the queue; the four siblings below are its, nil each where
	// it lacks one — and extender nil too unless AutoExtend asked for it.
	broker   corequeue.Broker
	waker    corequeue.Waker
	releaser corequeue.Releaser
	rejecter corequeue.Rejecter
	delayer  corequeue.DelayedNacker
	extender corequeue.LeaseExtender
	cfg      ConsumerConfig
}

// newConsumer asks broker for its siblings once, for every worker.
func newConsumer(broker corequeue.Broker, cfg ConsumerConfig) *consumer {
	engine := &consumer{broker: broker, cfg: cfg}
	engine.waker, _ = broker.(corequeue.Waker)
	engine.releaser, _ = broker.(corequeue.Releaser)
	engine.rejecter, _ = broker.(corequeue.Rejecter)
	engine.delayer, _ = broker.(corequeue.DelayedNacker)
	//: renewing is the caller's choice, and a capability the broker may lack.
	if cfg.AutoExtend {
		engine.extender, _ = broker.(corequeue.LeaseExtender)
	}
	//: shared by every worker, read-only from here.
	return engine
}

// pump is one worker: lease, process, acknowledge, repeat. It returns only
// when its context ends or the storage refuses.
func (c *consumer) pump(ctx context.Context) error {
	//: until the consumer is stopped or the medium gives up.
	for {
		//: checked first, so a cancelled consumer never leases a message it
		//: is about to abandon.
		if ctx.Err() != nil {
			//: a stopped consumer, not a failed one.
			return nil
		}
		var signal <-chan struct{}
		//: taken BEFORE the Receive: a publication landing between an empty
		//: Receive and the wait closes this channel, so it is not missed.
		if c.waker != nil {
			signal = c.waker.Wake().Signal
		}
		batch, receiveErr := c.broker.Receive(ctx, c.cfg.BatchSize)
		//: a cancelled Receive is the stop, not a storage failure.
		if receiveErr != nil {
			//: nil for the caller's own cancellation, the broker's error
			//: otherwise.
			return ignoreCancelled(ctx, receiveErr)
		}
		//: an empty queue is not an error and not a busy loop.
		if len(batch) == 0 {
			//: waits through the clock, never time.Sleep, and wakes on cancel,
			//: on a publication, or when something the broker holds is due.
			//: the instant the wait is measured from is read BEFORE the broker
			//: is asked, so a clock moving meanwhile can only make the wake
			//: early — a cheap empty Receive — and never late.
			from := c.cfg.Clock.Now()
			if !idle(ctx, c.cfg, signal, from.Add(idleFor(c.cfg.PollInterval, c.waker))) {
				//: cancelled while waiting.
				return nil
			}
			continue
		}
		//: sequentially: the batch is held under one lease each, and running
		//: them concurrently here would be a second, invisible parallelism
		//: knob beside cfg.Parallelism. The instant it arrived is what every
		//: lease of the batch is measured from.
		if settleErr := c.settleBatch(ctx, batch, c.cfg.Clock.Now()); settleErr != nil {
			//: the medium refused while reporting an outcome.
			return ignoreCancelled(ctx, settleErr)
		}
	}
}

// idleFor returns how long a worker that just found the queue empty may
// sleep: the poll interval, shortened to the moment the broker says a message
// it already holds becomes receivable — read AFTER the empty Receive, so it
// reflects what that Receive saw.
func idleFor(poll time.Duration, waker corequeue.Waker) time.Duration {
	//: a broker that cannot say is polled at the configured cadence.
	if waker == nil {
		//: the poll interval, unchanged.
		return poll
	}
	next := waker.Wake()
	//: nothing becomes receivable on its own: only an event or the poll.
	if !next.Scheduled || next.In >= poll {
		//: the poll bounds what the broker cannot see, such as another
		//: process's publication.
		return poll
	}
	//: due already, or sooner than the poll; never negative.
	return max(next.In, 0)
}

// idle waits for the first of: the caller's cancellation, the broker's wake
// signal, or the consumer's clock reaching until. It reports whether the
// worker should carry on.
//
// A nil signal — a broker without the Waker capability — blocks forever in the
// select and leaves the plain poll it always was.
func idle(ctx context.Context, cfg ConsumerConfig, signal <-chan struct{}, until time.Time) bool {
	//: a timer rather than After, so a wake that wins releases it at once
	//: instead of leaving it armed until it fires.
	timer := armUntil(cfg.Clock, until)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		//: stop.
		return false
	case <-signal:
		//: something was published or handed back: ask again now.
		return true
	case <-timer.C():
		//: the poll elapsed, or something became due: ask again.
		return true
	}
}

// armUntil returns a timer that fires when clk reaches until.
//
// A timer takes a duration, measured from the instant it registers, and that
// instant is not the one the duration was computed from: a clock that moves
// between the two — a test's ManualClock advancing while a worker arms its
// idle wait — would fire the timer that much late, after the instant the
// broker said a message is due, and a manual clock that is not advanced
// again never fires it at all (a mail spool's retry missed on OpenBSD this
// way). So the clock is read on both sides of the registration, and a timer
// armed across a move is armed again from the later reading; a real clock,
// which moves between any two readings, is re-armed a bounded number of times
// for a lateness of nanoseconds.
func armUntil(clk clock.Timed, until time.Time) clock.Timer {
	before := clk.Now()
	timer := clk.NewTimer(max(until.Sub(before), 0))
	//: re-armed while the clock moved across the registration.
	for range armTimerRounds {
		after := clk.Now()
		//: registered at the instant the duration was measured from.
		if !after.After(before) {
			break
		}
		timer.Stop()
		before = after
		timer = clk.NewTimer(max(until.Sub(before), 0))
	}
	//: due at until, or a few nanoseconds of a real clock after it.
	return timer
}

// settleBatch runs the handler over each delivery and reports each outcome to
// the broker, stopping only if the STORAGE refuses.
//
// A stop that lands in the middle of the batch hands every message not yet
// started back at once, uncounted (kitsunium/sdk#474): before, each was handed
// to the handler under a dead context and nacked on it, and every durable
// broker refused that nack, so the message stayed leased for a whole
// visibility timeout and came back one delivery closer to the dead-letter
// store — a deployment burnt a delivery of work nobody had tried. A message
// whose lease has run down to the margin before its turn comes is handed back
// the same way, or renewed first under AutoExtend.
func (c *consumer) settleBatch(ctx context.Context, batch []corequeue.DeliveryValue, receivedAt time.Time) error {
	//: one at a time, in the order the broker handed them over.
	for index, delivery := range batch {
		//: stopped: nothing more is started, and nothing is left leased.
		if ctx.Err() != nil {
			//: nil, or the medium refusing a hand-back.
			return c.handBackAll(ctx, batch[index:])
		}
		plan := c.planFor(delivery, receivedAt)
		ready, prepared, prepareErr := c.prepare(ctx, delivery, plan)
		//: the medium refused a renewal or a hand-back.
		if prepareErr != nil {
			//: QueueBackendFailed — the medium, not the message.
			return prepareErr
		}
		//: handed back, or already somebody else's.
		if !ready {
			continue
		}
		//: a lapsed lease is skipped rather than returned: the message is
		//: already back in the queue and somebody else's problem now.
		if err := c.process(ctx, prepared, plan); err != nil {
			//: QueueBackendFailed — the medium, not the message.
			return err
		}
	}
	//: every delivery in this batch has been acknowledged or handed back.
	return nil
}

// prepare decides, just before a message's turn, whether its lease still
// leaves the handler room: renewed first under AutoExtend once a third of it
// is spent, handed back when it is down to the margin, as it is otherwise. It
// reports whether to run the handler, and the delivery to run it on.
func (c *consumer) prepare(
	ctx context.Context, delivery corequeue.DeliveryValue, plan leasePlan,
) (ready bool, prepared corequeue.DeliveryValue, err error) {
	//: a lease that cannot be measured is not judged, as before the bound.
	if !plan.bounded {
		//: run as received.
		return true, delivery, nil
	}
	left := delivery.Lease.ExpiresAt.Sub(c.cfg.Clock.Now())
	//: a third spent: renewed before it starts, so it starts with a whole one.
	if c.extender != nil && left <= plan.granted-plan.renewEvery {
		lease, renewErr := c.renew(ctx, delivery.Lease.Receipt, plan.granted)
		//: the lease lapsed meanwhile: the message is somebody else's now.
		if kerrs.HasCode(renewErr, corequeue.CodeLeaseExpired) {
			//: skipped, as a lapsed acknowledgement is.
			return false, delivery, nil
		}
		//: the medium refused.
		if renewErr != nil {
			//: QueueBackendFailed, or the bound on it.
			return false, delivery, renewErr
		}
		delivery.Lease = lease
		//: run on the renewed lease.
		return true, delivery, nil
	}
	//: the handler would be stopped before it could start.
	if left <= plan.margin {
		//: handed back uncounted, and leased again by a later poll.
		return false, delivery, c.handBack(ctx, delivery.Lease.Receipt)
	}
	//: room enough.
	return true, delivery, nil
}

// process runs the handler on one delivery, watched, and reports its outcome.
func (c *consumer) process(ctx context.Context, delivery corequeue.DeliveryValue, plan leasePlan) error {
	watched := c.watch(ctx, delivery, plan)
	failure := guard(watched.ctx, c.cfg.Handler, delivery)
	lease, stopped := watched.finish(ctx, failure)
	//: the receipt AutoExtend last renewed to, which is the one that holds.
	return c.settle(ctx, lease.Receipt, failure, stopped)
}

// settle reports a handler's outcome to the broker.
//
// A LeaseExpired from the broker is swallowed on purpose and it is the only
// thing that is: this consumer took too long, the message is already queued
// again or dead-lettered, and there is nothing it can do about that except
// keep working. Anything else is the storage refusing, which the worker must
// not poll its way past.
//
// A failure the handler marked with core/data/queue.DoNotRetry is rejected
// rather than nacked when the broker is a core/data/queue.Rejecter:
// dead-lettered at once, with the handler's cause, instead of retried until
// MaxDeliveries. A broker without the sibling is nacked as before — the
// shortcut is lost, never the message — and a panic is never the mark,
// because guard carries the recovered value as a field of HANDLER_PANICKED and
// never as its origin.
//
// A failure once the consumer is stopping — its drain over, or the lease's
// margin reached during it — is not the message's: it is handed back
// uncounted, unless it is a panic, which is.
//
// A failure the handler marked with core/data/queue.RetryAfter is nacked
// with the delay it names when the broker is a core/data/queue.DelayedNacker
// (ADR 0183), its delivery counted, and nacked as any other failure — after
// the policy's delay — when it is not. DoNotRetry and a stop are read first,
// so a failure marked both ways is rejected, and one a stop cut short is
// handed back.
//
// Every outcome is sent on ctx WITHOUT its cancellation, bounded by
// SettleTimeout: the work is done, or was stopped, and a stop that lands
// between the handler's return and the report — the consumer's context
// cancelled by Close or a lifecycle's Stop — must not abandon it. Abandoned,
// the message stayed leased and came back after the visibility timeout, a
// duplicate a graceful stop caused, on every broker whose verbs honour their
// context (kitsunium/sdk#472, #474).
func (c *consumer) settle(ctx context.Context, receipt corequeue.ReceiptValue, failure error, stopped bool) error {
	//: the ordinary path, and the overwhelmingly common one.
	if failure == nil {
		//: the only call in the domain that removes a message, past a stop.
		return ignoreLapsed(c.bounded(ctx, "ack", false, func(settleCtx context.Context) error {
			//: removed.
			return c.broker.Ack(settleCtx, receipt)
		}))
	}
	//: no retry can fix it, and the broker can say so at once.
	if c.rejecter != nil && kerrs.HasCode(failure, corequeue.CodeNotRetryable) {
		//: same rule as the acknowledgement.
		return ignoreLapsed(c.bounded(ctx, "reject", false, func(settleCtx context.Context) error {
			//: dead-lettered with the handler's cause.
			return c.rejecter.Reject(settleCtx, receipt, failure)
		}))
	}
	//: the stop cut it short; the message did nothing wrong.
	if stopped && !kerrs.HasCode(failure, corequeue.CodeHandlerPanicked) {
		//: uncounted, receivable at once.
		return c.handBack(ctx, receipt)
	}
	//: the handler said when to try again, and the broker can wait that long.
	if delay, marked := corequeue.RetryAfterOf(failure); marked && c.delayer != nil {
		//: same rule as the acknowledgement.
		return ignoreLapsed(c.bounded(ctx, "nack", false, func(settleCtx context.Context) error {
			_, nackErr := c.delayer.NackAfter(settleCtx, receipt, failure, delay)
			//: retried after the handler's delay, or dead-lettered with its cause.
			return nackErr
		}))
	}
	//: the broker decides: retried, or abandoned with its cause.
	return c.nack(ctx, receipt, failure)
}

// handBackAll hands back, in order, every delivery of a batch the handler
// never started, stopping at the first the medium refuses.
func (c *consumer) handBackAll(ctx context.Context, rest []corequeue.DeliveryValue) error {
	//: each one, as the broker handed them over.
	for _, delivery := range rest {
		//: a medium that refused one will refuse the next, a SettleTimeout each.
		if err := c.handBack(ctx, delivery.Lease.Receipt); err != nil {
			//: QueueBackendFailed — read as the stop it happened during.
			return err
		}
	}
	//: nothing of the batch is left leased.
	return nil
}

// handBack returns a message this consumer did not finish: released — at
// once, its delivery not counted — by a broker that can, nacked by one that
// cannot, which retries it after its delay with the delivery counted, so the
// courtesy is lost and never the message. The nack carries no cause: nothing
// failed, and a message that runs out of attempts this way is recorded as an
// unreported failure.
func (c *consumer) handBack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	//: a broker without the sibling.
	if c.releaser == nil {
		//: retried after its delay, counted.
		return c.nack(ctx, receipt, nil)
	}
	//: receivable now, uncounted; a lapsed lease is already elsewhere.
	return ignoreLapsed(c.bounded(ctx, "release", false, func(settleCtx context.Context) error {
		//: handed back.
		return c.releaser.Release(settleCtx, receipt)
	}))
}

// nack hands a message back as a failure, bounded as every outcome is.
func (c *consumer) nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	//: same rule as the acknowledgement.
	return ignoreLapsed(c.bounded(ctx, "nack", false, func(settleCtx context.Context) error {
		_, nackErr := c.broker.Nack(settleCtx, receipt, cause)
		//: retried, or dead-lettered with its cause.
		return nackErr
	}))
}

// guard invokes the handler with its panic recovered.
//
// A panic escaping a handler would kill the consumer PROCESS, abandoning
// every other in-flight lease — each of which then has to time out, one
// visibility timeout at a time, before anything moves again. Recovered, it is
// an ordinary nack: this message is retried and eventually dead-lettered, and
// its siblings are untouched.
//
// The stack is captured inside the deferred recover, while the panicking
// frames are still unwinding. The recovered value travels as a FIELD and
// never as the wrap origin, so a panic carrying an *errs.Error cannot hijack
// HANDLER_PANICKED — the service/app/events and service/app/lifecycle rule.
func guard(
	ctx context.Context, handler corequeue.Handler, delivery corequeue.DeliveryValue,
) (failure error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: leave failure as the handler returned it.
			return
		}
		failure = kerrs.Wrap(corequeue.HandlerPanicked, kerrs.WrapParams{},
			kerrs.String("message", delivery.Message.ID),
			kerrs.String("panic", fmt.Sprint(value)), kerrs.String("stack", string(debug.Stack())))
	}()
	//: the handler's error travels to Nack UNMODIFIED, and that is a
	//: deliberate departure from service/app/events.
	//
	//: events joins its own ListenerFailed verdict beside the listener's
	//: cause because Publish RETURNS the aggregate, and a caller has to be
	//: able to ask "did anything fail?" without knowing every code every
	//: listener might produce. Here the aggregate's destination is a
	//: DEAD-LETTER RECORD, which already carries "a handler failed" by
	//: existing and has structured fields for the rest. A verdict wrapped or
	//: joined around the cause would only displace the reason an investigator
	//: actually needs — TestAFailingHandlerIsRetriedAndThenDeadLettered
	//: asserts that the handler's own reason is what reaches the store.
	return handler(ctx, delivery)
}

// ignoreLapsed reads a lapsed lease as a fact of life rather than a failure.
func ignoreLapsed(cause error) error {
	//: this consumer was too slow; the message is already elsewhere and there
	//: is nothing to do about it but carry on.
	if kerrs.HasCode(cause, corequeue.CodeLeaseExpired) {
		//: not the worker's verdict.
		return nil
	}
	//: nil, or the storage refusing.
	return cause
}

// ignoreCancelled reads a failure that is really this consumer's own stop as
// a stop.
func ignoreCancelled(ctx context.Context, cause error) error {
	//: the caller cancelled; the broker only said so.
	if ctx.Err() != nil {
		//: a stopped consumer, not a failed one.
		return nil
	}
	//: the storage genuinely refused, and polling will not fix it.
	return cause
}
