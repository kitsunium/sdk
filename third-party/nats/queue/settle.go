package queue

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// Ack acknowledges the leased message and waits for the server to confirm
// it (a double acknowledgement), sent only while the lease holds on the
// broker's clock.
func (b *jetStreamBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	lease, terr := b.leases.take(receipt, b.clock.Now())
	//: lapsed, settled, replaced or foreign: nothing is sent.
	if terr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return terr
	}
	ctx, cancel := b.call(ctx)
	defer cancel()
	//: the lease still holds when the server did not confirm.
	if aerr := lease.msg.DoubleAck(ctx); aerr != nil {
		b.leases.restore(receipt, lease)
		//: typed.
		return requestError(aerr, "ack")
	}
	//: removed: the stream is a work queue.
	return nil
}

// Nack hands the message back after the policy's retry delay
// (NakWithDelay), or dead-letters it on its last delivery.
func (b *jetStreamBroker) Nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error) (corequeue.NackValue, error) {
	//: the delay is the policy's, grown by the delivery that failed.
	return b.nack(ctx, receipt, cause, nil)
}

// NackAfter is Nack with the delay the handler names.
func (b *jetStreamBroker) NackAfter(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	//: past what an instant can carry.
	if delay > corequeue.MaxDeadlineOffset {
		//: QUEUE_MISCONFIGURED; the lease still holds.
		return corequeue.NackValue{}, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "delay"), errs.Int64("value_ns", int64(delay)))
	}
	//: a negative delay is now.
	return b.nack(ctx, receipt, cause, &delay)
}

// nack is Nack and NackAfter: delay nil means the policy's.
func (b *jetStreamBroker) nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay *time.Duration) (corequeue.NackValue, error) {
	now := b.clock.Now()
	lease, terr := b.leases.take(receipt, now)
	//: lapsed, settled, replaced or foreign: nothing is sent.
	if terr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return corequeue.NackValue{}, terr
	}
	//: the last attempt: the broker owns the decision to dead-letter.
	if lease.deliveries >= b.cfg.Policy.MaxDeliveries {
		return b.deadLetterLeased(ctx, receipt, lease, describeCause(cause))
	}
	wait := b.retryDelay(lease.deliveries)
	//: the handler's own delay replaces the policy's.
	if delay != nil {
		wait = max(*delay, 0)
	}
	//: the lease still holds when the server was not told.
	if nerr := lease.msg.NakWithDelay(wait); nerr != nil {
		b.leases.restore(receipt, lease)
		//: typed.
		return corequeue.NackValue{}, requestError(nerr, "nak")
	}
	visible := now.Add(wait)
	b.waker.signal(visible)
	//: handed back.
	return corequeue.NackValue{Deliveries: lease.deliveries, VisibleAt: visible}, nil
}

// retryDelay is the policy's wait after the deliveries-th delivery failed.
func (b *jetStreamBroker) retryDelay(deliveries int) time.Duration {
	//: no ceiling asked for: the constant delay.
	if b.cfg.Policy.MaxRetryDelay <= 0 {
		return b.cfg.Policy.RetryDelay
	}
	//: RetryDelay × 2^(deliveries−1), held at the ceiling.
	return backoff.Value{BaseDelay: b.cfg.Policy.RetryDelay, MaxDelay: b.cfg.Policy.MaxRetryDelay}.Delay(deliveries)
}

// Reject dead-letters the leased message at once, with cause.
func (b *jetStreamBroker) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	lease, terr := b.leases.take(receipt, b.clock.Now())
	//: lapsed, settled, replaced or foreign: nothing is sent.
	if terr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return terr
	}
	_, derr := b.deadLetterLeased(ctx, receipt, lease, describeCause(cause))
	//: moved, or left leased on failure.
	return derr
}

// deadLetterLeased moves a leased message and, when that fails, gives the
// lease back so the caller may retry.
func (b *jetStreamBroker) deadLetterLeased(ctx context.Context, receipt corequeue.ReceiptValue, lease *leaseValue, cause causeValue) (corequeue.NackValue, error) {
	//: a publication or a termination that failed leaves the message leased.
	if derr := b.deadLetter(ctx, lease, cause); derr != nil {
		b.leases.restore(receipt, lease)
		return corequeue.NackValue{}, derr
	}
	//: dead-lettered with the count it had.
	return corequeue.NackValue{Deliveries: lease.deliveries, DeadLettered: true}, nil
}

// Extend renews the lease: InProgress resets the server's AckWait, so the
// renewal lasts by or AckWait, whichever is shorter. The old receipt is void
// once it returns.
func (b *jetStreamBroker) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	//: the two readings of zero are opposites; past 2262 is unrecordable.
	if by <= 0 || by > corequeue.MaxDeadlineOffset {
		//: QUEUE_MISCONFIGURED; the lease still holds.
		return corequeue.LeaseValue{}, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "by"), errs.Int64("value_ns", int64(by)))
	}
	start := b.clock.Now()
	lease, perr := b.leases.peek(receipt, start)
	//: a lapsed lease is not renewed, even when nobody has taken it.
	if perr != nil {
		return corequeue.LeaseValue{}, perr
	}
	//: the old lease still holds when the server was not told.
	if ierr := lease.msg.InProgress(); ierr != nil {
		return corequeue.LeaseValue{}, requestError(ierr, "in progress")
	}
	deadline := start.Add(min(by, b.lease))
	renewed, rerr := b.leases.replace(receipt, start, &leaseValue{
		deadline: deadline, msg: lease.msg, message: lease.message, deliveries: lease.deliveries,
	})
	//: settled by another call while the renewal was in flight.
	if rerr != nil {
		return corequeue.LeaseValue{}, rerr
	}
	b.waker.schedule(deadline, 1)
	//: a new receipt, the old one void.
	return corequeue.LeaseValue{ExpiresAt: deadline, Receipt: renewed}, nil
}
