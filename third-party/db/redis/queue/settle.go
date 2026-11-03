package queue

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The settle script's answers.
const (
	statusOK   string = "OK"
	statusBack string = "BACK"
	statusDead string = "DEAD"
)

// decimal is the base of every number the scripts read.
const decimal int = 10

// trailSep joins a trail's codes in a dead letter's field.
const trailSep string = ","

// settle runs settleScript for receipt: op, then op's own arguments. It
// answers the status and the delivery count after it, or the refusal of a
// receipt whose lease no longer holds.
func (b *streamBroker) settle(ctx context.Context, receipt corequeue.ReceiptValue, now time.Time, op string, extra ...any) (status string, deliveries int, err error) {
	parsed, ok := parseReceipt(receipt)
	//: not a receipt of this package.
	if !ok {
		//: UNKNOWN_RECEIPT.
		return "", 0, unknownReceipt()
	}
	args := append([]any{op, parsed.id, parsed.token, micros(now), b.group}, extra...)
	reply, rerr := settleScript.Run(ctx, b.client, b.keys, args...).Slice()
	//: the script failed.
	if rerr != nil {
		//: typed.
		return "", 0, commandError(rerr, strings.ToUpper(op))
	}
	status, _ = reply[0].(string)
	count, _ := reply[1].(int64)
	//: settled, handed back, dead-lettered.
	if status == statusOK || status == statusBack || status == statusDead {
		return status, int(count), nil
	}
	//: GONE, REPLACED, LAPSED.
	return status, 0, leaseLost(status)
}

// Ack removes the leased entry: XACK and XDEL, in one script.
func (b *streamBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	_, _, err := b.settle(ctx, receipt, b.clock.Now(), "ack")
	//: removed, or refused.
	return err
}

// Nack hands the entry back after the policy's retry delay, or dead-letters
// it on its last delivery.
func (b *streamBroker) Nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error) (corequeue.NackValue, error) {
	parsed, _ := parseReceipt(receipt)
	//: the delay grows with the delivery that failed.
	return b.nack(ctx, receipt, cause, b.retryDelay(parsed.deliveries))
}

// NackAfter is Nack with the delay the handler names.
func (b *streamBroker) NackAfter(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	//: past what an instant can carry.
	if delay > corequeue.MaxDeadlineOffset {
		//: QUEUE_MISCONFIGURED; the lease still holds.
		return corequeue.NackValue{}, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "delay"), errs.Int64("value_ns", int64(delay)))
	}
	//: a negative delay is now.
	return b.nack(ctx, receipt, cause, max(delay, 0))
}

// nack hands the entry back, visible delay after now.
func (b *streamBroker) nack(ctx context.Context, receipt corequeue.ReceiptValue, cause error, delay time.Duration) (corequeue.NackValue, error) {
	now := b.clock.Now()
	visible := now.Add(delay)
	described := describeCause(cause)
	status, count, err := b.settle(ctx, receipt, now, "nack", micros(visible), b.policy.MaxDeliveries,
		described.reason, described.public, strconv.FormatUint(uint64(described.code), decimal), encodeTrail(described.trail))
	//: refused or failed: nothing moved.
	if err != nil {
		return corequeue.NackValue{}, err
	}
	//: the last attempt: moved with its cause.
	if status == statusDead {
		return corequeue.NackValue{Deliveries: count, DeadLettered: true}, nil
	}
	b.waker.signal(visible)
	//: handed back.
	return corequeue.NackValue{Deliveries: count, VisibleAt: visible}, nil
}

// retryDelay is the policy's wait after the deliveries-th delivery failed.
func (b *streamBroker) retryDelay(deliveries int) time.Duration {
	//: no ceiling asked for: the constant delay.
	if b.policy.MaxRetryDelay <= 0 {
		return b.policy.RetryDelay
	}
	//: RetryDelay × 2^(deliveries−1), held at the ceiling.
	return backoff.Value{BaseDelay: b.policy.RetryDelay, MaxDelay: b.policy.MaxRetryDelay}.Delay(deliveries)
}

// Reject dead-letters the leased entry at once, with cause.
func (b *streamBroker) Reject(ctx context.Context, receipt corequeue.ReceiptValue, cause error) error {
	described := describeCause(cause)
	_, _, err := b.settle(ctx, receipt, b.clock.Now(), "reject", described.reason, described.public,
		strconv.FormatUint(uint64(described.code), decimal), encodeTrail(described.trail))
	//: moved, or refused.
	return err
}

// Release hands the entry back at once with its delivery count given back.
func (b *streamBroker) Release(ctx context.Context, receipt corequeue.ReceiptValue) error {
	now := b.clock.Now()
	_, _, err := b.settle(ctx, receipt, now, "release")
	//: refused: nothing moved.
	if err != nil {
		return err
	}
	b.waker.signal(now)
	//: receivable now, by any consumer.
	return nil
}

// Extend renews the lease for by from now. The old receipt is void at once:
// the new lease carries a new token.
func (b *streamBroker) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	//: the two readings of zero are opposites; past 2262 is unrecordable.
	if by <= 0 || by > corequeue.MaxDeadlineOffset {
		//: QUEUE_MISCONFIGURED; the lease still holds.
		return corequeue.LeaseValue{}, errs.Wrap(corequeue.QueueMisconfigured, errs.WrapParams{},
			errs.String("field", "by"), errs.Int64("value_ns", int64(by)))
	}
	now := b.clock.Now()
	deadline := now.Add(by)
	token := randomHex()
	parsed, _ := parseReceipt(receipt)
	_, _, err := b.settle(ctx, receipt, now, "extend", micros(deadline), token)
	//: lapsed, settled, replaced or foreign.
	if err != nil {
		return corequeue.LeaseValue{}, err
	}
	b.waker.schedule(deadline, 1)
	renewed := receiptValue{id: parsed.id, token: token, deliveries: parsed.deliveries}
	//: a new receipt, the old one void.
	return corequeue.LeaseValue{ExpiresAt: deadline, Receipt: renewed.encode()}, nil
}

// encodeTrail writes a trail's codes in decimal, oldest first.
func encodeTrail(trail []uint32) string {
	parts := make([]string, len(trail))
	//: each code in decimal.
	for i, code := range trail {
		parts[i] = strconv.FormatUint(uint64(code), decimal)
	}
	//: "" for none.
	return strings.Join(parts, trailSep)
}
