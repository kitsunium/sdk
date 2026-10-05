package events

import (
	"context"
	"reflect"
)

// EventType identifies the events a subscription reacts to by their concrete
// Go TYPE. It is an alias for reflect.Type, so a caller never converts.
//
// The key is the type and not a name because a name is unchecked: two
// packages can both pick "user.created", a rename silently unhooks every
// listener, and nothing fails until production is quiet. A Go type is minted
// by the compiler, cannot collide across packages, and is renamed by the same
// tool that renames its uses. The price is that an event crossing a process
// boundary has no identity here — which is the frontier in the package
// documentation, not a limitation to work around.
//
// A subscription's EventType MUST be a CONCRETE type. [Bus.Publish] resolves
// an event's type with reflect.TypeOf, which returns the value's DYNAMIC type
// and never an interface type, so a listener registered on an interface could
// never fire. That registration is refused rather than accepted and silently
// starved — see [InvalidEventType].
type EventType = reflect.Type

// Listener reacts to one published event. The dynamic type of event is
// exactly the [EventType] the subscription was registered for, so a listener
// may assert it unconditionally — internal/service/app/events.On does that for
// the caller and is the only registration path most code needs.
//
// Delivery is SYNCHRONOUS on the publisher's goroutine: a Listener that
// blocks blocks the publisher, and a Listener that takes a lock takes it in
// the publisher's lock order. Keep it short, or hand off to something that
// owns its own goroutine — and if what you actually need is "return now,
// finish later, reliably", see the package documentation's frontier.
//
// Returning [Halt] stops the dispatch: the listeners after this one in
// priority order are not called. Only a subscription that declared
// [SubscriptionValue.MayHalt] can do that; from any other listener it is a
// HALT_NOT_PERMITTED failure and the dispatch continues.
//
// Returning any other non-nil error is COLLECTED and the dispatch CONTINUES.
// The other listeners are independent consequences of the same fact; making
// their delivery depend on this one's disk being full would reintroduce
// exactly the coupling a bus exists to remove.
//
// ctx is the publisher's context, passed through untouched. The bus never
// inspects it: an event is a fact that has already happened, and cancelling
// half of its consequences produces the partial state the synchronous
// contract exists to prevent. A Listener that must honour cancellation holds
// the context and can.
type Listener func(ctx context.Context, event any) error
