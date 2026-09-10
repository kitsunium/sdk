package events_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcev "github.com/kitsunium/sdk/internal/service/events"
)

// TestASubscriptionWithNoNameIsRefused — a nameless listener cannot be
// reported on and no Unsubscribe could ever name it.
func TestASubscriptionWithNoNameIsRefused(t *testing.T) {
	t.Parallel()
	err := svcev.On(svcev.New(), svcev.Handler[orderPlaced]{
		Handle: func(context.Context, orderPlaced) error { return nil },
	})
	if !kerrs.HasCode(err, corev.CodeInvalidSubscription) {
		t.Fatalf("On with no Name: got %v, want INVALID_SUBSCRIPTION", err)
	}
}

// TestASubscriptionWithNoHandleIsRefused — the refusal happens at the TYPED
// call site, where the omission is.
func TestASubscriptionWithNoHandleIsRefused(t *testing.T) {
	t.Parallel()
	err := svcev.On(svcev.New(), svcev.Handler[orderPlaced]{Name: "orphan"})
	if !kerrs.HasCode(err, corev.CodeInvalidSubscription) {
		t.Fatalf("On with no Handle: got %v, want INVALID_SUBSCRIPTION", err)
	}
}

// TestAnInterfaceEventTypeIsRefusedAtRegistration is ADR 0031 applied to this
// domain, and the reason the refusal exists at all: Publish routes on a
// value's DYNAMIC type, which is never an interface, so such a subscription
// would be accepted, listed, and never called once.
func TestAnInterfaceEventTypeIsRefusedAtRegistration(t *testing.T) {
	t.Parallel()
	err := svcev.On(svcev.New(), svcev.Handler[error]{
		Name:   "never-fires",
		Handle: func(context.Context, error) error { return nil },
	})
	if !kerrs.HasCode(err, corev.CodeInvalidEventType) {
		t.Fatalf("On[error]: got %v, want INVALID_EVENT_TYPE", err)
	}
}

// TestTheEmptyInterfaceIsRefusedToo closes the obvious way around the rule:
// On[any] would otherwise register a listener for "everything" that matches
// nothing.
func TestTheEmptyInterfaceIsRefusedToo(t *testing.T) {
	t.Parallel()
	err := svcev.On(svcev.New(), svcev.Handler[any]{
		Name:   "catch-all",
		Handle: func(context.Context, any) error { return nil },
	})
	if !kerrs.HasCode(err, corev.CodeInvalidEventType) {
		t.Fatalf("On[any]: got %v, want INVALID_EVENT_TYPE", err)
	}
}

// TestANilEventTypeIsRefused covers the raw Bus.Subscribe path, which a
// caller who erased the type themselves reaches.
func TestANilEventTypeIsRefused(t *testing.T) {
	t.Parallel()
	err := svcev.New().Subscribe(nil, corev.SubscriptionValue{
		Name:     "orphan",
		Listener: func(context.Context, any) error { return nil },
	})
	if !kerrs.HasCode(err, corev.CodeInvalidEventType) {
		t.Fatalf("Subscribe(nil): got %v, want INVALID_EVENT_TYPE", err)
	}
}

// TestADuplicateNameIsRefused — names are how an error and an Unsubscribe
// identify a listener, so two sharing one would make every report ambiguous.
func TestADuplicateNameIsRefused(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("audit", corev.PriorityNormal, nil))

	err := svcev.On(bus, rec.handler("audit", 5, nil))

	if !kerrs.HasCode(err, corev.CodeDuplicateListener) {
		t.Fatalf("duplicate On: got %v, want DUPLICATE_LISTENER", err)
	}
	publish(t, bus, orderPlaced{id: 1})
	assertCalls(t, rec.snapshot(), []string{"audit"})
}

// TestTheSameNameOnTwoEventTypesIsFine — uniqueness is per event type, not
// per bus. "audit" is exactly the name two different listeners want.
func TestTheSameNameOnTwoEventTypesIsFine(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("audit", corev.PriorityNormal, nil))
	shipped := svcev.Handler[orderShipped]{
		Name:   "audit",
		Handle: func(context.Context, orderShipped) error { rec.note("audit:shipped"); return nil },
	}
	if err := svcev.On(bus, shipped); err != nil {
		t.Fatalf("On(audit for orderShipped): %v", err)
	}

	publish(t, bus, orderPlaced{id: 1})
	if _, err := bus.Publish(context.Background(), orderShipped{id: 2}); err != nil {
		t.Fatalf("Publish(shipped): %v", err)
	}

	assertCalls(t, rec.snapshot(), []string{"audit", "audit:shipped"})
}

// TestOffRemovesTheListener is the removal half of the contract.
func TestOffRemovesTheListener(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("audit", -1, nil), rec.handler("cache", 1, nil))

	if err := svcev.Off[orderPlaced](bus, "audit"); err != nil {
		t.Fatalf("Off: %v", err)
	}

	publish(t, bus, orderPlaced{id: 1})
	assertCalls(t, rec.snapshot(), []string{"cache"})
}

// TestOffOnAnUnknownNameSaysSo — "removed" and "there was nothing to remove"
// are different answers, and a caller who gets the second while expecting the
// first has a bug that silence would hide until the listener fired again.
func TestOffOnAnUnknownNameSaysSo(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("audit", corev.PriorityNormal, nil))

	err := svcev.Off[orderPlaced](bus, "typo")

	if !kerrs.HasCode(err, corev.CodeUnknownListener) {
		t.Fatalf("Off(typo): got %v, want UNKNOWN_LISTENER", err)
	}
}

// TestOffOnAnEmptyBusSaysSo covers the never-published membership, which is a
// separate code path from "the type has listeners but not this one".
func TestOffOnAnEmptyBusSaysSo(t *testing.T) {
	t.Parallel()
	err := svcev.Off[orderPlaced](svcev.New(), "nobody")
	if !kerrs.HasCode(err, corev.CodeUnknownListener) {
		t.Fatalf("Off on an empty bus: got %v, want UNKNOWN_LISTENER", err)
	}
}

// TestReSubscribingAfterOffIsAllowed proves the name is genuinely released
// and not merely hidden — a removal that leaves a tombstone would refuse this.
func TestReSubscribingAfterOffIsAllowed(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("audit", corev.PriorityNormal, nil))
	if err := svcev.Off[orderPlaced](bus, "audit"); err != nil {
		t.Fatalf("Off: %v", err)
	}

	on(t, bus, rec.handler("audit", corev.PriorityNormal, nil))

	publish(t, bus, orderPlaced{id: 1})
	assertCalls(t, rec.snapshot(), []string{"audit"})
}

// TestTheTypedListenerReceivesTheEventTyped is what the erasure buys back:
// the caller's function never sees an any.
func TestTheTypedListenerReceivesTheEventTyped(t *testing.T) {
	t.Parallel()
	got := 0
	bus := svcev.New()
	if err := svcev.On(bus, svcev.Handler[orderPlaced]{
		Name:   "reader",
		Handle: func(_ context.Context, event orderPlaced) error { got = event.id; return nil },
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	publish(t, bus, orderPlaced{id: 7})

	if got != 7 {
		t.Fatalf("listener saw id %d, want 7", got)
	}
}

// TestTheContextReachesTheListenerUntouched — the bus never inspects it, and
// a listener that must honour cancellation has to be able to.
func TestTheContextReachesTheListenerUntouched(t *testing.T) {
	t.Parallel()
	type key struct{}
	bus := svcev.New()
	var seen any
	if err := svcev.On(bus, svcev.Handler[orderPlaced]{
		Name:   "reader",
		Handle: func(ctx context.Context, _ orderPlaced) error { seen = ctx.Value(key{}); return nil },
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	ctx := context.WithValue(context.Background(), key{}, "carried")
	if _, err := bus.Publish(ctx, orderPlaced{id: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if seen != "carried" {
		t.Fatalf("context value: got %v, want %q", seen, "carried")
	}
}

// TestACancelledContextStillDeliversEverything is decision D7 stated as a
// test rather than as prose: an event is a fact that already happened, and
// abandoning half of its consequences is the partial state the synchronous
// contract exists to prevent.
func TestACancelledContextStillDeliversEverything(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("one", 1, nil), rec.handler("two", 2, nil), rec.handler("three", 3, nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := bus.Publish(ctx, orderPlaced{id: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	assertCalls(t, rec.snapshot(), []string{"one", "two", "three"})
}

// TestSubscribingDuringADispatchNeverPanicsOrDeadlocks is what the
// copy-on-write membership buys. The listener that mutates the bus is running
// INSIDE the dispatch that is ranging over it — the shape that turns a
// naively-locked bus into a deadlock and a naively-mutated slice into a race.
func TestSubscribingDuringADispatchNeverPanicsOrDeadlocks(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	reentrant := svcev.Handler[orderPlaced]{
		Name: "reentrant",
		Handle: func(_ context.Context, _ orderPlaced) error {
			rec.note("reentrant")
			//: a duplicate on the second publish is expected and ignored.
			ignoreErr(svcev.On(bus, rec.handler("late", 100, nil)))
			//: and remove one, from inside the very list being walked.
			ignoreErr(svcev.Off[orderPlaced](bus, "doomed"))
			return nil
		},
	}
	if err := svcev.On(bus, reentrant); err != nil {
		t.Fatalf("On: %v", err)
	}
	on(t, bus, rec.handler("doomed", 50, nil))

	publish(t, bus, orderPlaced{id: 1})
	publish(t, bus, orderPlaced{id: 2})

	//: first dispatch walks the snapshot it loaded: reentrant, then doomed.
	//: second walks the rewritten one: reentrant, then late.
	assertCalls(t, rec.snapshot(), []string{"reentrant", "doomed", "reentrant", "late"})
}

// TestConcurrentPublishAndRegistrationIsRaceFree is the -race guard on the
// snapshot discipline. It asserts no outcome beyond "nothing exploded",
// because with churn this dense no count is deterministic.
func TestConcurrentPublishAndRegistrationIsRaceFree(t *testing.T) {
	t.Parallel()
	bus := svcev.New()
	on(t, bus, newRecorder().handler("steady", corev.PriorityNormal, nil))
	var wg sync.WaitGroup
	const rounds int = 200

	wg.Add(3)
	go func() {
		defer wg.Done()
		for range rounds {
			ignoreDispatch(bus.Publish(context.Background(), orderPlaced{id: 1}))
		}
	}()
	go func() {
		defer wg.Done()
		rec := newRecorder()
		for range rounds {
			ignoreErr(svcev.On(bus, rec.handler("churn", 1, nil)))
			ignoreErr(svcev.Off[orderPlaced](bus, "churn"))
		}
	}()
	go func() {
		defer wg.Done()
		for range rounds {
			ignoreDispatch(bus.Publish(context.Background(), orderShipped{id: 2}))
		}
	}()
	wg.Wait()
}

// TestSubscribeKeysOnTheTypeReflectTypeForResolves pins the routing key to
// the same expression On uses, so a future refactor that keys on a name or a
// pointer type is caught here rather than in production silence.
func TestSubscribeKeysOnTheTypeReflectTypeForResolves(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	sub := corev.SubscriptionValue{
		Name:     "raw",
		Listener: func(context.Context, any) error { rec.note("raw"); return nil },
	}
	if err := bus.Subscribe(reflect.TypeFor[orderPlaced](), sub); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	publish(t, bus, orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"raw"})
}

// ignoreErr discards an error a test deliberately does not assert on.
//
// It exists so a reentrant handler can register and deregister from inside the
// very dispatch that is walking the list — the point under test is that the
// walk survives the mutation, not what those two calls return. Written as a
// named function rather than `_ =` so the intent is greppable: every call site
// is a place where an error is expected and irrelevant, and a reader can find
// them all at once.
func ignoreErr(error) {}

// ignoreDispatch discards both halves of a Publish a test does not assert on.
//
// The concurrency cases publish only to create traffic against a list another
// goroutine is mutating; what the dispatch reported is beside the point, and
// the race detector is the actual assertion. Named for the same reason as
// [ignoreErr]: a discarded result should be findable, not invisible.
func ignoreDispatch(corev.DispatchValue, error) {}
