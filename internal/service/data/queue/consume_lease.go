package queue

import (
	"context"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// leaseMarginDivisor sets the margin a handler is stopped by before its lease
// ends: a tenth of the lease, capped at SettleTimeout — room for the nack of a
// handler that ran out of time to land while the lease still holds.
const leaseMarginDivisor time.Duration = 10

// renewDivisor sets AutoExtend's cadence: a renewal every third of the lease,
// which leaves room for two consecutive failures before it lapses — the
// cadence lock's Keepalive recommends for the same reason.
const renewDivisor time.Duration = 3

// leasePlan is how one delivery's lease is judged: how long the broker
// granted it, the margin the handler is stopped by, and how often AutoExtend
// renews it.
type leasePlan struct {
	// granted is the lease's length as the broker granted it, read on the
	// consumer's clock when the batch arrived; it is also what a renewal
	// asks for.
	granted time.Duration
	// margin is how long before the lease ends the handler is stopped.
	margin time.Duration
	// renewEvery is AutoExtend's cadence.
	renewEvery time.Duration
	// bounded is false when the lease already looked ended on the consumer's
	// clock as it arrived — two clocks that disagree — and is then not judged.
	bounded bool
}

// planFor measures delivery's lease from the instant its batch arrived.
func (c *consumer) planFor(delivery corequeue.DeliveryValue, receivedAt time.Time) leasePlan {
	granted := delivery.Lease.ExpiresAt.Sub(receivedAt)
	//: a broker never grants a lease that has already ended: the clocks
	//: disagree, and a bound read on the wrong one would stop every handler.
	if granted <= 0 {
		//: unbounded, as Consume was before the bound.
		return leasePlan{}
	}
	//: a tenth, capped, and a renewal every third — never a zero period.
	return leasePlan{
		granted: granted, margin: min(granted/leaseMarginDivisor, c.cfg.SettleTimeout),
		renewEvery: max(granted/renewDivisor, time.Nanosecond), bounded: true,
	}
}

// watched is one handler run under watch: the context the handler gets, and
// what watches it.
type watched struct {
	// ctx is the handler's context.
	ctx context.Context
	// cancel ends ctx with a cause; nil when ctx is the consumer's own.
	cancel context.CancelCauseFunc
	// handled ends when the handler returns; markHandled ends it, and may be
	// called more than once.
	handled     context.Context
	markHandled context.CancelFunc
	// deadline fires a margin before the lease ends; renewal ticks
	// AutoExtend; drain fires DrainTimeout after the stop. Each is nil when
	// unused, and every one is stopped by finish.
	deadline clock.Timer
	renewal  clock.Ticker
	drain    clock.Timer
	// stop is the consumer's stop until it is seen, nil after; drained is
	// the drain's channel once it is armed. The watcher's alone.
	stop    <-chan struct{}
	drained <-chan time.Time
	// id names the message in a cause.
	id string
	// lease is the lease the handler runs under, renewed by the watcher.
	// The watcher owns it, and stopped, until watcher.Wait returns.
	lease corequeue.LeaseValue
	// plan is how the lease is judged.
	plan leasePlan
	// watcher is the watching goroutine.
	watcher sync.WaitGroup
	// stopped reports that the consumer stopped while the handler ran: a
	// failure that follows is the stop's, whether the drain, the stop itself
	// or the lease's margin during the drain ended the handler.
	stopped bool
}

// watch prepares the handler's context for delivery. When there is nothing
// to watch — no lease to bound, no drain, so no renewal either — it is the
// consumer's own, as it always was; otherwise one goroutine watches the stop,
// the drain, the lease's margin and AutoExtend's cadence.
//
// Goroutine lifecycle: at most one, started here and joined by finish, which
// process calls on every path once the handler has returned. Its timers are
// armed HERE, before the handler starts, so a test driving a ManualClock
// finds them armed once the handler runs; finish stops every one.
func (c *consumer) watch(ctx context.Context, delivery corequeue.DeliveryValue, plan leasePlan) *watched {
	w := &watched{ctx: ctx, lease: delivery.Lease, plan: plan, id: delivery.Message.ID, stop: ctx.Done()}
	//: nothing to bound and nothing to drain: the consumer's context, as before.
	if !plan.bounded && c.cfg.DrainTimeout <= 0 {
		//: no goroutine, no timer.
		return w
	}
	//: the stop reaches the handler through the watcher, which may let it drain.
	w.ctx, w.cancel = context.WithCancelCause(context.WithoutCancel(ctx))
	w.handled, w.markHandled = context.WithCancel(context.WithoutCancel(ctx))
	//: a margin before the lease ends, on the consumer's clock.
	if plan.bounded {
		w.deadline = c.cfg.Clock.NewTimer(delivery.Lease.ExpiresAt.Sub(c.cfg.Clock.Now()) - plan.margin)
	}
	//: a renewal every third of the lease, while the handler runs.
	if plan.bounded && c.extender != nil {
		w.renewal = c.cfg.Clock.NewTicker(plan.renewEvery)
	}
	w.watcher.Go(func() {
		//: until the handler returns or its context is ended.
		for c.oversee(ctx, w) {
		}
	})
	//: the handler's context, watched.
	return w
}

// oversee is one turn of the watcher: it ends the handler's context on the
// consumer's stop — at once, or once DrainTimeout has passed — and a margin
// before the lease ends, and renews the lease under AutoExtend, moving that
// margin with it. It reports whether to keep watching.
func (c *consumer) oversee(ctx context.Context, w *watched) bool {
	select {
	case <-w.handled.Done():
		//: the handler returned first; finish takes it from here.
		return false
	case <-w.stop:
		w.stop = nil
		w.stopped = true
		//: a drain lets the message in hand finish; armed now, stopped by finish.
		if c.cfg.DrainTimeout > 0 {
			w.drain = c.cfg.Clock.NewTimer(c.cfg.DrainTimeout)
			w.drained = w.drain.C()
			//: watching the drain from here.
			return true
		}
		//: no drain: the stop reaches the handler now, as it always did.
		return w.halt(context.Cause(ctx))
	case <-w.drained:
		//: the drain is over.
		return w.halt(context.Cause(ctx))
	case <-timerC(w.deadline):
		//: past this instant another consumer may hold the message.
		return w.halt(kerrs.Wrap(corequeue.LeaseExpired, kerrs.WrapParams{},
			kerrs.String("message", w.id), kerrs.String("why", "the handler's lease ends within its margin")))
	case <-tickerC(w.renewal):
		//: renewed, or the handler told why not.
		return c.renewWatched(ctx, w)
	}
}

// renewWatched renews the lease a watched handler runs under and moves its
// margin with it, or cancels the handler with the refusal: its work is
// becoming a duplicate, and it hears so through its context.
func (c *consumer) renewWatched(ctx context.Context, w *watched) bool {
	lease, renewErr := c.renew(ctx, w.lease.Receipt, w.plan.granted)
	//: the lease is lost, or the medium refused.
	if renewErr != nil {
		//: the refusal is the cause.
		return w.halt(renewErr)
	}
	w.lease = lease
	w.deadline.Reset(lease.ExpiresAt.Sub(c.cfg.Clock.Now()) - w.plan.margin)
	//: the margin moved with the lease.
	return true
}

// halt cancels the handler's context with cause and ends the watch.
func (w *watched) halt(cause error) bool {
	w.cancel(cause)
	//: the handler is stopping; nothing more to watch.
	return false
}

// finish ends the watch once the handler has returned with failure: it joins
// the watcher, stops every timer it armed and releases the context. It
// returns the lease the handler ended under — renewed or not — and whether
// the consumer's stop cut the handler short.
func (w *watched) finish(ctx context.Context, failure error) (lease corequeue.LeaseValue, stopped bool) {
	//: unwatched: the handler ran on the consumer's own context.
	if w.cancel == nil {
		//: a failure after the stop is the stop's.
		return w.lease, failure != nil && ctx.Err() != nil
	}
	w.markHandled()
	w.watcher.Wait()
	stopTimer(w.deadline)
	stopTimer(w.drain)
	//: a ticker has its own Stop.
	if w.renewal != nil {
		w.renewal.Stop()
	}
	w.cancel(context.Canceled)
	//: read only now that the watcher has exited.
	return w.lease, w.stopped && failure != nil
}

// timerC is timer's channel, or nil — which blocks forever in a select — when
// there is no timer.
func timerC(timer clock.Timer) <-chan time.Time {
	//: not watched.
	if timer == nil {
		//: never ready.
		return nil
	}
	//: the timer's.
	return timer.C()
}

// tickerC is ticker's channel, or nil when there is no ticker.
func tickerC(ticker clock.Ticker) <-chan time.Time {
	//: not watched.
	if ticker == nil {
		//: never ready.
		return nil
	}
	//: the ticker's.
	return ticker.C()
}

// stopTimer stops timer when it was armed at all.
func stopTimer(timer clock.Timer) {
	//: an unused timer is nil.
	if timer != nil {
		timer.Stop()
	}
}

// renew extends the lease receipt names by by, bounded as every outcome is.
func (c *consumer) renew(
	ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration,
) (lease corequeue.LeaseValue, err error) {
	err = c.bounded(ctx, "extend", true, func(settleCtx context.Context) error {
		var extendErr error
		lease, extendErr = c.extender.Extend(settleCtx, receipt, by)
		//: renewed, LeaseExpired, or the medium refusing.
		return extendErr
	})
	//: the new lease, or why there is none.
	return lease, err
}

// bounded runs call on ctx WITHOUT its cancellation, bounded by SettleTimeout
// on the consumer's clock from the instant the consumer stops — at once when
// it already has, or when always is set, as a renewal's is. A call the bound
// cut short is QUEUE_BACKEND_FAILED naming op — the medium did not answer in
// time — and any other answer is call's own.
//
// While the consumer runs, an outcome is not timed: nothing waits on it but
// the worker, which the medium's own failure reaches, and an idle worker's
// clock then holds no wait but its poll — what a test driving a ManualClock
// counts on with BlockUntil.
//
// Goroutine lifecycle: one per call waits for the stop and then the bound's
// timer. It ends when call returns (done closes) or when the timer fires and
// cancels call's context, and bounded joins it before returning, so none
// outlives the call.
func (c *consumer) bounded(ctx context.Context, op string, always bool, call func(context.Context) error) error {
	settleCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	var armed clock.Timer
	//: armed here, before the call, so a test finds it armed while it runs.
	if always || ctx.Err() != nil {
		armed = c.cfg.Clock.NewTimer(c.cfg.SettleTimeout)
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go c.bound(ctx, armed, done, exited, func() {
		//: the medium did not answer within the bound.
		cancel(kerrs.Wrap(corequeue.QueueBackendFailed, kerrs.WrapParams{},
			kerrs.String("op", op), kerrs.String("settle_timeout", c.cfg.SettleTimeout.String())))
	})
	err := call(settleCtx)
	close(done)
	<-exited
	//: read before the release below cancels it for good.
	cut, cause := settleCtx.Err() != nil, context.Cause(settleCtx)
	cancel(context.Canceled)
	//: answered, or refused for a reason of the medium's own.
	if err == nil || !cut {
		//: call's own answer.
		return err
	}
	//: the bound cut it short.
	return cause
}

// bound is bounded's goroutine: it arms the bound when the consumer stops,
// unless armed already is, and calls expire when the bound elapses before
// done closes. It stops the timer it waited on, whichever way it returns.
func (c *consumer) bound(ctx context.Context, armed clock.Timer, done <-chan struct{}, exited chan<- struct{}, expire func()) {
	defer close(exited)
	//: not armed yet: the bound starts with the stop.
	if armed == nil {
		select {
		case <-done:
			//: answered while the consumer ran.
			return
		case <-ctx.Done():
			armed = c.cfg.Clock.NewTimer(c.cfg.SettleTimeout)
		}
	}
	defer armed.Stop()
	select {
	case <-armed.C():
		expire()
	case <-done:
		//: answered in time.
	}
}
