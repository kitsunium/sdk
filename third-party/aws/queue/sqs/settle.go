package sqs

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// Ack deletes the leased message: one DeleteMessage, sent only while the
// lease holds on the broker's clock.
func (b *sqsBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	lease, terr := b.leases.take(receipt, b.clock.Now())
	//: lapsed, settled, replaced or foreign: nothing is sent.
	if terr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return terr
	}
	_, derr := b.api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(b.queueURL), ReceiptHandle: aws.String(lease.handle)})
	//: the lease still holds: the caller may try again.
	if derr != nil {
		b.leases.restore(receipt, lease)
		//: ACK_FAILED.
		return requestFailed(derr, AckFailed.Params(), "DeleteMessage")
	}
	//: removed.
	return nil
}

// Nack hands the message back after the policy's retry delay, or
// dead-letters it on its last delivery.
func (b *sqsBroker) Nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error) (corequeue.NackValue, error) {
	//: the delay is the policy's, grown by the delivery that failed.
	return b.nack(ctx, receipt, cause, nil)
}

// NackAfter is Nack with the delay the handler names, up to MaxVisibility.
func (b *sqsBroker) NackAfter(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	//: SQS keeps a message invisible twelve hours at most.
	if delay > MaxVisibility {
		//: nothing moved; the lease still holds.
		return corequeue.NackValue{}, delayTooLong("delay", int64(delay), int64(MaxVisibility))
	}
	//: a negative delay is now.
	return b.nack(ctx, receipt, cause, &delay)
}

// nack is Nack and NackAfter: delay nil means the policy's.
func (b *sqsBroker) nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay *time.Duration) (corequeue.NackValue, error) {
	now := b.clock.Now()
	lease, terr := b.leases.take(receipt, now)
	//: lapsed, settled, replaced or foreign: nothing is sent.
	if terr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return corequeue.NackValue{}, terr
	}
	//: the last attempt: the broker owns the decision to dead-letter.
	if lease.deliveries >= b.policy.MaxDeliveries {
		//: moved with its cause, or left leased on failure.
		return b.deadLetterLeased(ctx, receipt, lease, describeCause(cause))
	}
	wait := b.retryDelay(lease.deliveries)
	//: the handler's own delay replaces the policy's.
	if delay != nil {
		wait = max(*delay, 0)
	}
	secs := seconds(wait)
	_, cerr := b.api.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(b.queueURL), ReceiptHandle: aws.String(lease.handle), VisibilityTimeout: secs,
	})
	//: the lease still holds: the caller may try again.
	if cerr != nil {
		b.leases.restore(receipt, lease)
		//: ACK_FAILED.
		return corequeue.NackValue{}, requestFailed(cerr, AckFailed.Params(), "ChangeMessageVisibility")
	}
	//: visible again once SQS's whole seconds have passed.
	return corequeue.NackValue{Deliveries: lease.deliveries, VisibleAt: now.Add(time.Duration(secs) * time.Second)}, nil
}

// retryDelay is the policy's wait after the deliveries-th delivery failed:
// RetryDelay, grown by kernel/backoff's curve up to MaxRetryDelay when set.
func (b *sqsBroker) retryDelay(deliveries int) time.Duration {
	//: no ceiling asked for: the constant delay.
	if b.policy.MaxRetryDelay <= 0 {
		return b.policy.RetryDelay
	}
	//: RetryDelay × 2^(deliveries−1), held at the ceiling.
	return backoff.Value{BaseDelay: b.policy.RetryDelay, MaxDelay: b.policy.MaxRetryDelay}.Delay(deliveries)
}

// Reject dead-letters the leased message at once, with cause.
func (b *sqsBroker) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
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

// deadLetterLeased moves a leased message to the dead-letter queue and, when
// that fails, gives the lease back so the caller may retry.
func (b *sqsBroker) deadLetterLeased(ctx context.Context, receipt corequeue.ReceiptValue, lease *leaseValue, cause causeValue) (corequeue.NackValue, error) {
	//: a send or a delete that failed leaves the message in the queue.
	if derr := b.deadLetter(ctx, lease, cause); derr != nil {
		b.leases.restore(receipt, lease)
		//: SEND_FAILED or ACK_FAILED.
		return corequeue.NackValue{}, derr
	}
	//: dead-lettered with the count it had.
	return corequeue.NackValue{Deliveries: lease.deliveries, DeadLettered: true}, nil
}

// Extend renews the lease for by from now: one ChangeMessageVisibility. The
// old receipt is void once it returns.
func (b *sqsBroker) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	//: the two readings of zero are opposites.
	if by <= 0 {
		//: QUEUE_MISCONFIGURED.
		return corequeue.LeaseValue{}, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "by"), errs.Int64("value_ns", int64(by)))
	}
	//: SQS keeps a message invisible twelve hours at most.
	if by > MaxVisibility {
		//: nothing moved; the lease still holds.
		return corequeue.LeaseValue{}, delayTooLong("by", int64(by), int64(MaxVisibility))
	}
	start := b.clock.Now()
	lease, perr := b.leases.peek(receipt, start)
	//: a lapsed lease is not renewed, even when nobody has taken it.
	if perr != nil {
		//: LEASE_EXPIRED or UNKNOWN_RECEIPT.
		return corequeue.LeaseValue{}, perr
	}
	_, cerr := b.api.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(b.queueURL), ReceiptHandle: aws.String(lease.handle), VisibilityTimeout: seconds(by),
	})
	//: the old lease still holds.
	if cerr != nil {
		//: ACK_FAILED.
		return corequeue.LeaseValue{}, requestFailed(cerr, AckFailed.Params(), "ChangeMessageVisibility")
	}
	deadline := start.Add(by)
	renewed, rerr := b.leases.replace(receipt, start, &leaseValue{
		deadline: deadline, handle: lease.handle, message: lease.message, deliveries: lease.deliveries,
	})
	//: settled by another call while the renewal was in flight.
	if rerr != nil {
		//: LEASE_EXPIRED.
		return corequeue.LeaseValue{}, rerr
	}
	//: a new receipt, the old one void.
	return corequeue.LeaseValue{ExpiresAt: deadline, Receipt: renewed}, nil
}
