package events_test

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	svcev "github.com/kitsunium/sdk/internal/service/events"
)

// errSink keeps a returned error alive so neither the compiler nor the linter
// treats the call as discardable. Benchmarks are single-goroutine, so a
// package-level sink is safe here in a way it would not be in the concurrency
// tests.
var errSink error

// sink keeps a listener's work observable to the compiler so the whole
// dispatch cannot be optimised away.
var sink int

// benchEvent is the published value. A two-word struct: big enough that
// boxing it into an any is a real allocation, small enough that the number
// measures the bus and not a memcpy.
type benchEvent struct {
	// id is the payload the listener reads.
	id int
	// tag is a second word, so the value is not pointer-shaped.
	tag int
}

// typedHandler builds the ordinary registration: a typed function, erased by
// On, asserted back on delivery.
//
// Its body is deliberately the SAME statement erasedSubscription's is, so the
// delta between the two benchmarks is the assertion and nothing else.
func typedHandler(name string, priority corev.Priority) svcev.Handler[benchEvent] {
	return svcev.Handler[benchEvent]{
		Name:     name,
		Priority: priority,
		Handle:   func(context.Context, benchEvent) error { sink++; return nil },
	}
}

// erasedSubscription builds the SAME work with no assertion at all: the
// listener reads the any and never converts. It is not something a caller
// would write — it is the control that isolates the assertion's cost from the
// dispatch machinery around it.
func erasedSubscription(name string) corev.SubscriptionValue {
	return corev.SubscriptionValue{
		Name:     name,
		Listener: func(_ context.Context, _ any) error { sink++; return nil },
	}
}

// busWith returns a bus carrying n typed listeners at distinct priorities.
func busWith(b *testing.B, n int) corev.Bus {
	b.Helper()
	bus := svcev.New()
	for i := range n {
		if err := svcev.On(bus, typedHandler("listener-"+strconv.Itoa(i), corev.Priority(i))); err != nil {
			b.Fatalf("On: %v", err)
		}
	}
	return bus
}

// BenchmarkDirectCall is the floor: the same closure, called directly, with
// no bus between the publisher and the listener. Every other number here is
// read against this one.
func BenchmarkDirectCall(b *testing.B) {
	handle := func(context.Context, benchEvent) error { sink++; return nil }
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		errSink = handle(ctx, event)
	}
}

// BenchmarkPublish_OneTypedListener is the ordinary path: reflect.TypeOf, one
// map lookup, the panic guard, the assertion, the call.
func BenchmarkPublish_OneTypedListener(b *testing.B) {
	bus := busWith(b, 1)
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, event)
	}
}

// BenchmarkPublish_OneErasedListener is the control for the assertion. Same
// dispatch, same guard, same lookup — the listener simply never converts.
// The delta against BenchmarkPublish_OneTypedListener IS the price of
// heterogeneity.
func BenchmarkPublish_OneErasedListener(b *testing.B) {
	bus := svcev.New()
	if err := bus.Subscribe(typeOfBenchEvent(), erasedSubscription("erased")); err != nil {
		b.Fatalf("Subscribe: %v", err)
	}
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, event)
	}
}

// BenchmarkPublish_EightTypedListeners shows what the per-listener cost is,
// once the fixed cost of a dispatch has been paid.
func BenchmarkPublish_EightTypedListeners(b *testing.B) {
	bus := busWith(b, 8)
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, event)
	}
}

// BenchmarkPublish_FreshEvent is the honest publisher-side number. Every
// other benchmark here publishes a loop-invariant value, which the compiler
// hoists the interface conversion out of — so they report 0 allocs and hide
// the one cost a real call site cannot avoid: boxing a struct into an any.
// Here the event is built from the iteration, so the box is paid per publish.
func BenchmarkPublish_FreshEvent(b *testing.B) {
	bus := busWith(b, 1)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		_, errSink = bus.Publish(ctx, benchEvent{id: i, tag: i})
	}
}

// BenchmarkPublish_NoListener is the cost a publisher pays for an event
// nobody has subscribed to — the number that decides whether publishing
// unconditionally is affordable.
func BenchmarkPublish_NoListener(b *testing.B) {
	bus := busWith(b, 1)
	ctx := context.Background()
	other := struct{ n int }{n: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, other)
	}
}

// BenchmarkPublish_HaltAtFirst measures the short-circuit against eight
// registered listeners: seven of them must cost nothing.
func BenchmarkPublish_HaltAtFirst(b *testing.B) {
	bus := busWith(b, 8)
	guard := svcev.Handler[benchEvent]{
		Name:     "guard",
		Priority: -1,
		MayHalt:  true,
		Handle:   func(context.Context, benchEvent) error { return corev.Halt },
	}
	if err := svcev.On(bus, guard); err != nil {
		b.Fatalf("On: %v", err)
	}
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, event)
	}
}

// BenchmarkPublish_OneListenerFails is the failing path, kept separate
// because it is the one that allocates: the aggregate, the verdict and its
// fields all exist only when something went wrong.
func BenchmarkPublish_OneListenerFails(b *testing.B) {
	bus := svcev.New()
	failing := svcev.Handler[benchEvent]{
		Name:   "broken",
		Handle: func(context.Context, benchEvent) error { return errDisk },
	}
	if err := svcev.On(bus, failing); err != nil {
		b.Fatalf("On: %v", err)
	}
	ctx := context.Background()
	event := benchEvent{id: 1, tag: 2}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, errSink = bus.Publish(ctx, event)
	}
}

// BenchmarkSubscribe measures membership churn against an eight-listener
// index — the side of the copy-on-write trade that pays for the free read.
func BenchmarkSubscribe(b *testing.B) {
	bus := busWith(b, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := svcev.On(bus, typedHandler("churn", 100)); err != nil {
			b.Fatalf("On: %v", err)
		}
		if err := svcev.Off[benchEvent](bus, "churn"); err != nil {
			b.Fatalf("Off: %v", err)
		}
	}
}

// typeOfBenchEvent returns the routing key On computes for benchEvent, so the
// erased control registers under exactly the key the typed path uses.
func typeOfBenchEvent() corev.EventType { return reflect.TypeFor[benchEvent]() }
