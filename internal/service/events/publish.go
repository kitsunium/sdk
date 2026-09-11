// Package events — hosts the dispatch: the ordered walk, the halt, the panic
// guard, and the aggregate.
package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Publish calls every listener registered for the event's dynamic type, in
// ascending priority and then registration order, ON THIS GOROUTINE, and
// returns when the last one has returned.
//
// ctx is passed to every listener and is never inspected by the bus. An event
// is a fact that has already happened; abandoning half of its consequences
// because the publisher's deadline expired would produce exactly the partial
// state the synchronous contract exists to prevent. The price is stated
// rather than hidden: a dispatch does not end early, so a listener that must
// honour cancellation has to do it itself — and a caller who wants the bus to
// return before the work finishes wants a queue, not this.
//
// A bus with no listener for this type is legitimate: the zero DispatchValue
// and a nil error. A publisher must not have to know whether anybody is
// listening.
func (b *bus) Publish(ctx context.Context, event any) (report corev.DispatchValue, err error) {
	//: the dynamic type IS the routing key; nil means the caller published an
	//: untyped nil, which no subscription could ever have been keyed on.
	eventType := reflect.TypeOf(event)
	//: an untyped nil has no type to route on.
	if eventType == nil {
		//: refuse rather than silently delivering to nobody, which is
		//: indistinguishable from a bus that is wired wrong.
		return corev.DispatchValue{}, kerrs.Wrap(corev.InvalidEventType, kerrs.WrapParams{},
			kerrs.String("at", "publish"), kerrs.String("kind", "nil"))
	}
	//: one atomic load, no lock and no allocation — and the copy-on-write
	//: membership is what lets a listener subscribe or leave DURING this
	//: dispatch without stopping it.
	current := b.members.Load()
	//: nothing has ever been registered on this bus.
	if current == nil {
		//: publishing into an empty bus is a no-op, not an error.
		return corev.DispatchValue{}, nil
	}
	//: a type nobody listens for reads the same way, and must.
	return dispatch(ctx, eventType, event, current.byType[eventType])
}

// dispatch walks the ordered subscriptions, stopping only for an authorised
// halt, and aggregates what came back.
//
// A failure never short-circuits. Listeners are INDEPENDENT consequences of
// one fact, and making the third one's delivery depend on the second one's
// disk being full would reintroduce precisely the coupling a bus removes.
func dispatch(
	ctx context.Context, eventType corev.EventType, event any, subs []corev.SubscriptionValue,
) (report corev.DispatchValue, aggregate error) {
	//: nil until something actually fails, so errors.Join hands back a
	//: genuine nil and the ordinary dispatch allocates nothing.
	var failures []error
	//: ascending priority, then registration order — the slice is already in it.
	for index, sub := range subs {
		err := call(ctx, eventType, sub, event)
		//: the ordinary path, and the overwhelmingly common one.
		if err == nil {
			report.Delivered++
			continue
		}
		//: a halt ends the walk; anything else is collected and the walk
		//: continues.
		if classify(&report, &failures, sub, eventType, err) {
			report.Skipped = len(subs) - index - 1
			//: a halt is a decision, not a failure: whatever else went wrong
			//: is still reported, but the halt itself is not an error.
			return report, errors.Join(failures...)
		}
	}
	//: nil for an empty slice — the zero-failure dispatch returns no error.
	return report, errors.Join(failures...)
}

// classify sorts one non-nil listener outcome, updates the report and the
// failure list, and reports whether the dispatch must stop here.
func classify(
	report *corev.DispatchValue, failures *[]error,
	sub corev.SubscriptionValue, eventType corev.EventType, err error,
) bool {
	//: a panic never reaches this classification as a halt: it is not a
	//: decision, and a listener that crashed did not choose anything.
	if kerrs.HasCode(err, corev.CodeListenerPanicked) {
		report.Failed++
		*failures = append(*failures, err)
		//: the remaining listeners are not broken; carry on.
		return false
	}
	//: the listener ran and returned, whatever it returned.
	report.Delivered++
	//: everything that is not the control sentinel is an ordinary failure.
	if !kerrs.HasCode(err, corev.CodeHalt) {
		report.Failed++
		*failures = append(*failures, joinListenerFailure(sub.Name, eventType, err))
		//: collected; the next listener still gets the event.
		return false
	}
	//: the sentinel from a listener that never declared the authority.
	if !sub.MayHalt {
		report.Failed++
		*failures = append(*failures, kerrs.Wrap(HaltNotPermitted, kerrs.WrapParams{},
			kerrs.String("listener", sub.Name), kerrs.String("event", eventType.String())))
		//: the permission means something: propagation continues.
		return false
	}
	report.Halted = true
	report.HaltedBy = sub.Name
	//: an authorised halt, and the only thing that stops the walk.
	return true
}

// joinListenerFailure puts the bus's verdict and the listener's own error
// side by side.
//
// errs.Wrap would hit the origin-wins rule and inherit the listener's code,
// so "did any listener fail?" would only be answerable by someone who already
// knew every code every listener might produce. Joined, errs.HasCode(err,
// CodeListenerFailed) and the caller's own errors.Is both answer.
func joinListenerFailure(name string, eventType corev.EventType, err error) error {
	//: verdict first, cause second — the order errors.Join renders them in.
	return errors.Join(kerrs.Wrap(ListenerFailed, kerrs.WrapParams{},
		kerrs.String("listener", name), kerrs.String("event", eventType.String())), err)
}

// call invokes one listener with its panic recovered.
//
// A panic escaping a listener would kill the PUBLISHER — which, in a
// synchronous same-goroutine bus, is the caller's request or transaction, and
// is the one party that did nothing wrong. It would also silently cancel
// every listener after this one, so a single broken observer would decide
// what the rest of the application gets to see.
//
// The stack is captured here, inside the deferred recover, while the
// panicking frames are still unwinding. kernel/group needed a PanicValue type
// to carry that across a goroutine boundary; there is no boundary here — the
// listener ran on this goroutine — so the stack goes straight into a field
// and the recovered value becomes an ordinary typed error.
func call(
	ctx context.Context, eventType corev.EventType, sub corev.SubscriptionValue, event any,
) (err error) {
	defer func() {
		value := recover()
		//: the ordinary path.
		if value == nil {
			//: leave err as the listener returned it.
			return
		}
		//: the recovered value travels as a FIELD, never as the wrap origin,
		//: so a panic carrying an *errs.Error cannot hijack LISTENER_PANICKED.
		err = kerrs.Wrap(corev.ListenerPanicked, kerrs.WrapParams{},
			kerrs.String("listener", sub.Name), kerrs.String("event", eventType.String()),
			kerrs.String("panic", fmt.Sprint(value)), kerrs.String("stack", string(debug.Stack())))
	}()
	//: the listener gets the publisher's context, untouched.
	return sub.Listener(ctx, event)
}
