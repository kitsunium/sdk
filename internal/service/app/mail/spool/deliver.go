package spool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"runtime/debug"
	"time"

	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The SMTP reply codes RFC 5321 §4.2.1 calls permanent negative completion:
// 5yz.
const (
	permanentReplyMin int = 500
	permanentReplyMax int = 599
)

// deliver is the spool's queue handler. Returning nil acknowledges; returning
// an error nacks, which on the last attempt dead-letters the mail with that
// error. A failure giveUp condemns is returned marked DoNotRetry, which the
// consumer turns into the queue's Reject: dead-lettered on this attempt.
//
// A failure with attempts left does neither: the lease is extended by the
// backoff and the handler returns nil. The extension replaced the receipt, so
// the consumer's acknowledgement of the old one is refused LeaseExpired —
// which the consumer ignores, as it ignores every lapsed acknowledgement — and
// the mail comes back when the new lease lapses, its delivery counted.
func (s *Spool) deliver(ctx context.Context, delivery corequeue.DeliveryValue) error {
	var record spooledValue
	//: a record that is not a spooled mail fails like a delivery, and ends as
	//: a dead letter an investigator can still read.
	if decodeErr := json.Unmarshal(delivery.Message.Payload, &record); decodeErr != nil {
		//: MessageUndecodable, naming the queue message.
		return kerrs.Wrap(corespool.MessageUndecodable, kerrs.WrapParams{}, kerrs.String("message", delivery.Message.ID))
	}
	attempt := delivery.Deliveries
	event := EventValue{ID: record.ID, Message: record.Message, Meta: record.Meta, QueuedAt: record.QueuedAt, Attempt: attempt}
	//: delivered already: this is the queue's redelivery, never a new mail.
	if s.delivered.has(record.ID) {
		event.Kind, event.At = EventDuplicate, s.clock.Now()
		s.report(&event)
		//: acknowledged, not sent.
		return nil
	}
	sendErr := s.attempt(ctx, &record, attempt)
	event.At = s.clock.Now()
	//: the transport accepted it.
	if sendErr == nil {
		s.delivered.add(record.ID)
		event.Kind = EventSent
		s.report(&event)
		//: acknowledged.
		return nil
	}
	event.Err = sendErr
	//: a failure no retry may repeat, or a mail too old to retry: rejected
	//: now, whatever attempts are left.
	if given := s.giveUp(sendErr, record.QueuedAt, event.At); given != nil {
		event.Err, event.Kind = given, EventDeadLettered
		s.report(&event)
		//: the consumer rejects it through the queue's Rejecter.
		return corequeue.DoNotRetry(given)
	}
	//: the last attempt: the nack dead-letters the mail with this failure.
	if attempt >= s.maxAttempts {
		event.Kind = EventDeadLettered
		s.report(&event)
		//: nacked, and the queue's delivery count says it is the last.
		return sendErr
	}
	delay := s.backoff.Delay(attempt)
	event.Kind, event.Next = EventRetrying, event.At.Add(delay)
	//: parked for the backoff: the lease lapses when the next attempt is due.
	if s.park(ctx, delivery, delay) {
		s.report(&event)
		//: nothing to acknowledge; see the doc comment.
		return nil
	}
	//: a queue that cannot extend a lease: its own retry delay instead.
	event.Next = event.At.Add(s.backoff.BaseDelay)
	s.report(&event)
	//: nacked.
	return sendErr
}

// attempt hands the mail to the transport, with the attempt in the context
// and bounded by SendTimeout on the spool's clock.
//
// Goroutine lifecycle: one per attempt waits on the timeout timer. It ends
// when the attempt returns (finished closes) or when the timer fires and
// cancels the attempt's context; attempt closes finished on every way out.
func (s *Spool) attempt(ctx context.Context, record *spooledValue, attempt int) error {
	sendCtx, cancel := context.WithCancel(withAttempt(ctx, AttemptValue{
		QueuedAt: record.QueuedAt, Meta: record.Meta, ID: record.ID, Attempt: attempt,
	}))
	defer cancel()
	timer := s.clock.NewTimer(s.sendTimeout)
	defer timer.Stop()
	finished := make(chan struct{})
	defer close(finished)
	//: the timeout, on the spool's clock.
	go func() {
		select {
		//: the attempt ran out of time: the transport must stop now.
		case <-timer.C():
			cancel()
		//: the attempt returned first.
		case <-finished:
		}
	}()
	//: the transport honours its context: it stops when the timeout cancels it.
	return s.send(sendCtx, record)
}

// send hands the mail to the transport and turns a panic into
// TransportPanicked, the attempt's failure like any other.
func (s *Spool) send(ctx context.Context, record *spooledValue) (err error) {
	defer func() {
		recovered := recover()
		//: the ordinary path.
		if recovered == nil {
			return
		}
		//: the value and the stack as fields, never the origin.
		err = kerrs.Wrap(corespool.TransportPanicked, kerrs.WrapParams{},
			kerrs.String("mail", record.ID), kerrs.String("panic", fmt.Sprint(recovered)),
			kerrs.String("stack", string(debug.Stack())))
	}()
	//: one hand-over.
	return s.transport.Send(ctx, record.Message)
}

// park extends the delivery's lease by delay and reports whether it could: a
// queue without the LeaseExtender capability cannot, and neither can a lease
// that already lapsed.
func (s *Spool) park(ctx context.Context, delivery corequeue.DeliveryValue, delay time.Duration) bool {
	extender, extendable := s.broker.(corequeue.LeaseExtender)
	//: both queues the spool builds can.
	if !extendable {
		//: the caller nacks instead.
		return false
	}
	_, extendErr := extender.Extend(ctx, delivery.Lease.Receipt, delay)
	//: a lease that lapsed meanwhile is already back in the queue.
	return extendErr == nil
}

// giveUp returns the failure a mail is dead-lettered with at once, or nil to
// let the backoff and MaxAttempts decide — always nil over a broker that is
// not a core/data/queue.Rejecter. A permanent SMTP reply is
// MailRejected unless RetryPermanent left s.permanent nil; a failure once
// MaxAge has passed since queuedAt is MailExpired. Either is added to
// sendErr, which stays the origin when it is an SDK error and stays reachable
// through errors.As when not.
func (s *Spool) giveUp(sendErr error, queuedAt, failedAt time.Time) error {
	//: a caller's queue that cannot dead-letter early would put the mail back
	//: after an EventDeadLettered: it is retried, and dead-lettered after its
	//: last attempt, as before.
	if _, rejects := s.broker.(corequeue.Rejecter); !rejects {
		//: the shortcut is lost, never the mail.
		return nil
	}
	//: a refusal the receiving server made for good.
	if s.permanent != nil && s.permanent(sendErr) {
		//: marked, the transport's failure kept.
		return mark(sendErr, corespool.MailRejected)
	}
	//: a mail that outlived its age: its next attempt never comes.
	if s.maxAge > 0 && !failedAt.Before(queuedAt.Add(s.maxAge)) {
		//: marked, the last failure kept.
		return mark(sendErr, corespool.MailExpired, kerrs.String("max_age", s.maxAge.String()))
	}
	//: retried, or dead-lettered after the last attempt.
	return nil
}

// permanentReply reports whether err carries an SMTP reply of the 5yz class
// anywhere in its chain: the SDK's SMTP transport keeps it reachable for a
// refused MAIL, RCPT or DATA, and a connector may return one as it is.
func permanentReply(err error) bool {
	reply, replied := errors.AsType[*textproto.Error](err)
	//: no reply, a 4yz, or anything else: temporary.
	return replied && reply.Code >= permanentReplyMin && reply.Code <= permanentReplyMax
}

// mark adds sentinel to cause as core/data/queue.DoNotRetry adds its own: an
// SDK cause keeps its Reason, Code and Public and gains the sentinel's code in
// its trail, and any other cause takes the sentinel as its origin and stays
// its source.
func mark(cause error, sentinel *kerrs.Error, fields ...kerrs.FieldValue) error {
	//: one call for both paths, by errs.Wrap's own rule.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code: sentinel.Code(), Reason: sentinel.Reason(), Public: sentinel.Public(),
		Private: sentinel.Private(), ExitCode: sentinel.ExitCode(),
	}, fields...)
}
