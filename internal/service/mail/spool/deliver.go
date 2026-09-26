// Package spool — one delivery: the record read back, a duplicate dropped,
// the attempt bounded, and its failure parked, or dead-lettered.
package spool

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// deliver is the spool's queue handler. Returning nil acknowledges; returning
// an error nacks, which on the last attempt dead-letters the mail with that
// error.
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
		return kerrs.Wrap(MessageUndecodable, kerrs.WrapParams{}, kerrs.String("message", delivery.Message.ID))
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
		err = kerrs.Wrap(TransportPanicked, kerrs.WrapParams{},
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
