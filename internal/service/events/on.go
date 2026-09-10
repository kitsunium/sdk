// Package events — hosts the TYPED registration front end: the generic
// functions that hide the erasure the heterogeneous bus is built on.
package events

import (
	"context"
	"reflect"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Handler is one typed listener's registration: the [On] argument.
//
// It is the typed front of core/events.SubscriptionValue, field for field.
// The duplication is the layer: core owns the ERASED contract a bus can
// actually dispatch, and this owns the shape a caller writes. Collapsing them
// would mean either putting a type parameter in the core port — which would
// make the bus hold exactly one event type — or making every caller write
// their own assertion.
type Handler[E any] struct {
	// Name identifies the listener in every error field and is the handle
	// [Off] removes it by. It must be non-empty and unique among the
	// listeners registered for E.
	Name string
	// Priority orders this listener among the others registered for E. Lower
	// runs first; equal priorities run in registration order. Its zero value,
	// core/events.PriorityNormal, is a working default.
	Priority corev.Priority
	// MayHalt authorises Handle to stop the dispatch by returning
	// core/events.Halt. False — the zero value — means it cannot.
	MayHalt bool
	// Handle is the reaction. It receives the event ALREADY TYPED. It must be
	// non-nil.
	Handle func(ctx context.Context, event E) error
}

// On registers a typed listener for the event type E.
//
// It is a package-level FUNCTION and not a method on Bus because Go methods
// cannot take type parameters of their own. That is not a workaround, it is
// the whole design constraint: a method-based Subscribe[E] would force the
// type parameter onto the Bus itself, and a Bus[E] carries exactly one event
// type — which is a channel, not a bus. See ADR 0053 §D3.
//
//	err := events.On(bus, events.Handler[OrderPlaced]{
//		Name:     "audit",
//		Priority: -10,
//		Handle:   func(ctx context.Context, e OrderPlaced) error { return audit.Write(ctx, e) },
//	})
//
// E MUST be a concrete type. An interface E is refused here rather than
// registered and never called: Publish resolves an event's type with
// reflect.TypeOf, which never yields an interface type (ADR 0031).
func On[E any](bus corev.Bus, handler Handler[E]) error {
	//: refuse the nil reaction at the TYPED call site, where the omission is,
	//: rather than letting an erased nil reach the bus's own validation and
	//: report a shape the caller never wrote.
	if handler.Handle == nil {
		//: the field says which part is missing, in the caller's own vocabulary.
		return kerrs.Wrap(corev.InvalidSubscription, kerrs.WrapParams{},
			kerrs.String("missing", "Handle"), kerrs.String("listener", handler.Name))
	}
	//: reflect.TypeFor resolves E at the call site, so the routing key and the
	//: assertion below can never disagree about which type this listener wants.
	return bus.Subscribe(reflect.TypeFor[E](), corev.SubscriptionValue{
		Name:     handler.Name,
		Priority: handler.Priority,
		MayHalt:  handler.MayHalt,
		Listener: erase(handler.Handle),
	})
}

// Off removes the listener registered under name for the event type E.
//
// It exists so that removing a listener is spelled with the same type
// parameter that registered it. Without it a caller would have to write
// reflect.TypeFor[E]() by hand — the one place reflection would have leaked
// out of this package and into application code.
func Off[E any](bus corev.Bus, name string) error {
	//: the same key On computed, from the same expression.
	return bus.Unsubscribe(reflect.TypeFor[E](), name)
}

// erase wraps a typed handler in the core/events.Listener the bus dispatches.
//
// The assertion is the ENTIRE cost of a heterogeneous bus in Go, and it is
// one comparison of two type pointers — measured against a direct call in
// BENCH.md. Its failure branch is unreachable through this package's API: On
// keys the subscription on reflect.TypeFor[E](), Publish only calls the
// listeners filed under reflect.TypeOf(event), and the bus refuses an
// interface E — so the dynamic type IS E whenever this runs. It is still
// checked, because a comma-ok that is never false costs nothing and a silent
// zero value would cost a listener firing on an event it never received.
func erase[E any](handle func(ctx context.Context, event E) error) corev.Listener {
	//: the closure IS the adapter; it captures nothing but the typed handler.
	return func(ctx context.Context, event any) error {
		typed, ok := event.(E)
		//: unreachable through On/Publish — see the doc comment.
		if !ok {
			//: name what arrived, so a future caller who reached this by
			//: registering an erased listener by hand can see why.
			return kerrs.Wrap(corev.InvalidEventType, kerrs.WrapParams{},
				kerrs.String("at", "deliver"), kerrs.String("event", reflect.TypeOf(event).String()),
				kerrs.String("want", reflect.TypeFor[E]().String()))
		}
		//: the listener never sees an any.
		return handle(ctx, typed)
	}
}
