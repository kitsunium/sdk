// Package queue — the consumer engine: the pull loop that runs a Handler on a
// CONSUMER'S goroutine, which is the third axis of ADR 0053's frontier.
package queue

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

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
func Consume(ctx context.Context, broker corequeue.Broker, cfg ConsumerConfig) error {
	//: the assertion the SDK cannot check, refused at the wiring site.
	if invalid := cfg.validate(); invalid != nil {
		//: ConsumerMisconfigured, naming the field.
		return invalid
	}
	resolved := cfg.normalized()
	//: a worker that fails for a STORAGE reason takes the others down with
	//: it, because they are all polling the same broken medium.
	stop, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make([]error, resolved.Parallelism)
	//: WaitGroup.Go owns the Add/Done pairing, so a worker cannot leak the
	//: counter by returning down a path that forgot it.
	var running sync.WaitGroup
	//: one independent pull loop per worker; they coordinate only through the
	//: broker, which is the thing that already arbitrates.
	for worker := range resolved.Parallelism {
		running.Go(func() {
			failures[worker] = pump(stop, broker, resolved)
			//: a storage failure stops every sibling, not just this one.
			if failures[worker] != nil {
				cancel()
			}
		})
	}
	running.Wait()
	//: nil unless a worker hit the storage; a cancelled consumer is a stopped
	//: one, not a failed one.
	return errors.Join(failures...)
}

// pump is one worker: lease, process, acknowledge, repeat. It returns only
// when its context ends or the storage refuses.
func pump(ctx context.Context, broker corequeue.Broker, cfg ConsumerConfig) error {
	//: until the consumer is stopped or the medium gives up.
	for {
		//: checked first, so a cancelled consumer never leases a message it
		//: is about to abandon.
		if ctx.Err() != nil {
			//: a stopped consumer, not a failed one.
			return nil
		}
		batch, receiveErr := broker.Receive(ctx, cfg.BatchSize)
		//: a cancelled Receive is the stop, not a storage failure.
		if receiveErr != nil {
			//: nil for the caller's own cancellation, the broker's error
			//: otherwise.
			return ignoreCancelled(ctx, receiveErr)
		}
		//: an empty queue is not an error and not a busy loop.
		if len(batch) == 0 {
			//: waits through the clock, never time.Sleep, and wakes on cancel.
			if !idle(ctx, cfg) {
				//: cancelled while waiting.
				return nil
			}
			continue
		}
		//: sequentially: the batch is held under one lease each, and running
		//: them concurrently here would be a second, invisible parallelism
		//: knob beside cfg.Parallelism.
		if settleErr := settleBatch(ctx, broker, cfg.Handler, batch); settleErr != nil {
			//: the medium refused while reporting an outcome.
			return ignoreCancelled(ctx, settleErr)
		}
	}
}

// idle waits out the poll interval and reports whether the worker should
// carry on.
func idle(ctx context.Context, cfg ConsumerConfig) bool {
	select {
	case <-ctx.Done():
		//: stop.
		return false
	case <-cfg.Clock.After(cfg.PollInterval):
		//: ask again.
		return true
	}
}

// settleBatch runs the handler over each delivery and reports each outcome to
// the broker, stopping only if the STORAGE refuses.
func settleBatch(
	ctx context.Context, broker corequeue.Broker,
	handler corequeue.Handler, batch []corequeue.DeliveryValue,
) error {
	//: one at a time, in the order the broker handed them over.
	for _, delivery := range batch {
		//: a lapsed lease is skipped rather than returned: the message is
		//: already back in the queue and somebody else's problem now.
		if err := settle(ctx, broker, handler, delivery); err != nil {
			//: QueueBackendFailed — the medium, not the message.
			return err
		}
	}
	//: every delivery in this batch has been acknowledged or handed back.
	return nil
}

// settle runs the handler and reports the outcome to the broker.
//
// A LeaseExpired from the acknowledgement is swallowed on purpose and it is
// the only thing that is: this consumer took too long, the message is already
// queued again or dead-lettered, and there is nothing it can do about that
// except keep working. Anything else is the storage refusing, which the
// worker must not poll its way past.
func settle(
	ctx context.Context, broker corequeue.Broker,
	handler corequeue.Handler, delivery corequeue.DeliveryValue,
) error {
	failure := guard(ctx, handler, delivery)
	//: the ordinary path, and the overwhelmingly common one.
	if failure == nil {
		//: the only call in the domain that removes a message.
		return ignoreLapsed(broker.Ack(ctx, delivery.Lease.Receipt))
	}
	//: the broker decides: retried, or abandoned with its cause.
	_, nackErr := broker.Nack(ctx, delivery.Lease.Receipt, failure)
	//: same rule as the acknowledgement.
	return ignoreLapsed(nackErr)
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
// HANDLER_PANICKED — the service/events and service/lifecycle rule.
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
		failure = kerrs.Wrap(HandlerPanicked, kerrs.WrapParams{},
			kerrs.String("message", delivery.Message.ID),
			kerrs.String("panic", fmt.Sprint(value)), kerrs.String("stack", string(debug.Stack())))
	}()
	//: the handler's error travels to Nack UNMODIFIED, and that is a
	//: deliberate departure from service/events.
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
