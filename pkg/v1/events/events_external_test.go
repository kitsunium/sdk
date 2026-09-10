package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/events"
)

// orderPlaced is the facade test's event type.
type orderPlaced struct {
	// id is the payload a listener reads back.
	id int
}

// TestTheFacadeDispatchesInPriorityOrder is the smoke test that the aliases
// and the two delegations are wired to the same engine.
func TestTheFacadeDispatchesInPriorityOrder(t *testing.T) {
	t.Parallel()
	var calls []string
	bus := events.New()
	record := func(name string) func(context.Context, orderPlaced) error {
		return func(context.Context, orderPlaced) error { calls = append(calls, name); return nil }
	}
	mustOn(t, bus, events.Handler[orderPlaced]{Name: "late", Priority: 10, Handle: record("late")})
	mustOn(t, bus, events.Handler[orderPlaced]{Name: "early", Priority: -10, Handle: record("early")})

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if len(calls) != 2 || calls[0] != "early" || calls[1] != "late" {
		t.Fatalf("call order: got %v, want [early late]", calls)
	}
	if report.Delivered != 2 {
		t.Fatalf("Delivered: got %d, want 2", report.Delivered)
	}
}

// TestHaltIsReachableThroughTheFacade — the control sentinel has to be
// re-exported, or a consumer could not stop a dispatch without importing an
// internal package they cannot import.
func TestHaltIsReachableThroughTheFacade(t *testing.T) {
	t.Parallel()
	bus := events.New()
	mustOn(t, bus, events.Handler[orderPlaced]{
		Name:    "guard",
		MayHalt: true,
		Handle:  func(context.Context, orderPlaced) error { return events.Halt },
	})
	mustOn(t, bus, events.Handler[orderPlaced]{
		Name:     "never",
		Priority: 1,
		Handle:   func(context.Context, orderPlaced) error { t.Error("ran after a halt"); return nil },
	})

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})
	if err != nil {
		t.Fatalf("a halt must not be an error: %v", err)
	}
	if !report.Halted || report.HaltedBy != "guard" || report.Skipped != 1 {
		t.Fatalf("report: got %+v, want halted by guard with 1 skipped", report)
	}
}

// TestAConsumerCanMatchTheSDKVerdictAndTheirOwnError is what the errors.Join
// aggregate buys, checked through the PUBLIC errs helpers a consumer has.
func TestAConsumerCanMatchTheSDKVerdictAndTheirOwnError(t *testing.T) {
	t.Parallel()
	mine := errors.New("my failure")
	bus := events.New()
	mustOn(t, bus, events.Handler[orderPlaced]{
		Name:   "broken",
		Handle: func(context.Context, orderPlaced) error { return mine },
	})

	_, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	if !errs.HasCode(err, events.ListenerFailed.Code()) {
		t.Fatalf("the SDK verdict is not matchable: %v", err)
	}
	if !errors.Is(err, mine) {
		t.Fatalf("the consumer's own error did not survive: %v", err)
	}
}

// TestAnInterfaceEventTypeIsRefusedThroughTheFacade keeps the ADR 0031
// refusal reachable from the surface a consumer actually uses.
func TestAnInterfaceEventTypeIsRefusedThroughTheFacade(t *testing.T) {
	t.Parallel()
	err := events.On(events.New(), events.Handler[error]{
		Name:   "never-fires",
		Handle: func(context.Context, error) error { return nil },
	})
	if !errors.Is(err, events.InvalidEventType) {
		t.Fatalf("On[error]: got %v, want InvalidEventType", err)
	}
}

// TestOffIsReachableThroughTheFacade covers the removal delegation, which is
// the one generic function a caller cannot spell without reflect otherwise.
func TestOffIsReachableThroughTheFacade(t *testing.T) {
	t.Parallel()
	bus := events.New()
	mustOn(t, bus, events.Handler[orderPlaced]{
		Name:   "audit",
		Handle: func(context.Context, orderPlaced) error { t.Error("ran after Off"); return nil },
	})

	if err := events.Off[orderPlaced](bus, "audit"); err != nil {
		t.Fatalf("Off: %v", err)
	}

	if _, err := bus.Publish(context.Background(), orderPlaced{id: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// mustOn registers a handler, failing the test on refusal.
func mustOn(t *testing.T, bus events.Bus, handler events.Handler[orderPlaced]) {
	t.Helper()
	if err := events.On(bus, handler); err != nil {
		t.Fatalf("On(%q): %v", handler.Name, err)
	}
}
