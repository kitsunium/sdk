//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/events .

// Package events is the public facade for the SDK's IN-PROCESS event bus: one
// fact is published, and every listener registered for that fact's type reacts
// to it, in an order you choose, on your goroutine, before Publish returns.
//
//	bus := events.New()
//
//	_ = events.On(bus, events.Handler[OrderPlaced]{
//		Name:     "audit",
//		Priority: -10, // runs before the default-priority listeners
//		Handle:   func(ctx context.Context, e OrderPlaced) error { return audit.Write(ctx, e) },
//	})
//	_ = events.On(bus, events.Handler[OrderPlaced]{
//		Name:   "cache",
//		Handle: func(ctx context.Context, e OrderPlaced) error { return cache.InvalidateTag(ctx, e.CustomerID) },
//	})
//
//	report, err := bus.Publish(ctx, OrderPlaced{ID: 42, CustomerID: "c-7"})
//
// # This is not a queue, and the difference is the whole design
//
// Read this before anything else in the package. The two are confused
// constantly, and a bus that tries to be both guarantees neither.
//
//	                | events (this package)      | a queue
//	----------------|----------------------------|---------------------------
//	Scope           | one process                | many processes
//	Timing          | synchronous                | asynchronous
//	Goroutine       | the publisher's            | a consumer's
//	Transaction     | the publisher's            | its own
//	Durability      | none                       | the point of it
//	Retries / DLQ   | none                       | the point of it
//	If the process  | the event never happened   | the event is still there
//	dies mid-flight |                            |
//
// If you need "return now, finish later, and do not lose it", you need a
// queue. This package will not give you a worse version of one: there is no
// option to dispatch on another goroutine, because that would buy you
// asynchrony while quietly removing durability — work that can vanish with no
// record that it existed. Publish an event AND enqueue a job; they are
// different statements about different guarantees.
//
// What this package is for is the other case, which is far more common: one
// fact with several INDEPENDENT consequences that all belong to the caller's
// transaction. Writing the audit row, invalidating the cache and stamping the
// metric have nothing to say to each other, and the code that placed the order
// should not have to import all three.
//
// # Ordering
//
// Listeners for one event type run in ascending [Handler].Priority — lower
// first, as with every stdlib comparator — and listeners at the SAME priority
// run in registration order. Both halves are promises. A bus whose ties
// resolved by map iteration would run the same program in a different order
// every time, and a listener set that works today would be the same code that
// fails tomorrow.
//
// # Stopping the dispatch
//
// A listener stops the remaining listeners by returning [Halt]. Only a
// listener whose registration set [Handler].MayHalt may do so; from any other
// listener the attempt is refused as [HaltNotPermitted], the dispatch
// CONTINUES, and the refusal is reported.
//
// The permission is a field at the registration site rather than a power every
// listener has, because a veto is an authority and an authority nobody wrote
// down is one nobody reviews. `MayHalt: true` is in the diff, in the review
// and in grep, next to the name of the listener that holds it.
//
// A halt is not a failure. [Bus].Publish returns a nil error for a dispatch
// whose only remarkable event was a halt, and reports it through
// [Dispatch].Halted, [Dispatch].HaltedBy and [Dispatch].Skipped.
//
// # Typing
//
// Go has no covariance and its methods cannot take type parameters, so a
// typed listener registers on a multi-type bus through the package-level
// generic [On] rather than through a method:
//
//	events.On(bus, events.Handler[OrderPlaced]{…})
//
// [On] derives the routing key from E and wraps your typed function in the
// erased one the bus dispatches. Your listener never sees an `any`. The event
// type must be CONCRETE: an interface is refused at registration
// ([InvalidEventType]), because Publish routes on a value's dynamic type and a
// listener keyed on an interface would be registered and never called.
//
// # Errors and panics
//
// A listener error does NOT stop the dispatch. The listeners are independent
// consequences of one fact, and making the third one's delivery depend on the
// second one's disk being full is the coupling a bus exists to remove.
// Everything that failed is aggregated with errors.Join, carrying
// [ListenerFailed] and the listener's own error side by side — so
// errs.HasCode(err, CodeListenerFailed) and your own errors.Is both answer on
// the same value.
//
// A listener that PANICS does not take the publisher down and does not
// silence the listeners after it. The panic is recovered, reported as
// [ListenerPanicked] with the listener's name, the panic value and the
// originating stack in fields, and the dispatch continues.
//
// # Cancellation
//
// The bus passes ctx to every listener and never inspects it. An event is a
// fact that has already happened; abandoning half of its consequences because
// the publisher's deadline expired produces exactly the partial state the
// synchronous contract exists to prevent. A listener that must honour
// cancellation holds the context and can.
package events
