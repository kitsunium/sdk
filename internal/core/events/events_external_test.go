package events_test

import (
	"context"
	"reflect"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestListenerIsAFunctionNotAnInterface is the executable form of ADR 0039.
// A published interface cannot grow a method without breaking every
// downstream implementer at compile time; a func type cannot grow one at all,
// which is how this port satisfies the rule structurally rather than by
// promise. A contributor who "tidies up" by turning Listener into an
// interface fails here, before review.
func TestListenerIsAFunctionNotAnInterface(t *testing.T) {
	t.Parallel()
	if kind := reflect.TypeFor[corev.Listener]().Kind(); kind != reflect.Func {
		t.Fatalf("Listener kind: got %v, want %v", kind, reflect.Func)
	}
}

// TestBusIsFrozenAtThreeMethods guards the other half of ADR 0039: the Bus
// interface IS published through pkg/v1/events, so a fourth method breaks
// every downstream double. A new capability gets a sibling interface.
func TestBusIsFrozenAtThreeMethods(t *testing.T) {
	t.Parallel()
	if got := reflect.TypeFor[corev.Bus]().NumMethod(); got != 3 {
		t.Fatalf("Bus method count: got %d, want 3 — a new capability gets a sibling (ADR 0039)", got)
	}
}

// TestATwoMethodDoubleDoesNotSatisfyBus is the compile-time claim above, made
// observable: a double missing Publish must NOT satisfy Bus, which is exactly
// what makes adding a fourth method a breaking change for everyone else.
func TestATwoMethodDoubleDoesNotSatisfyBus(t *testing.T) {
	t.Parallel()
	if reflect.TypeFor[*partialBus]().Implements(reflect.TypeFor[corev.Bus]()) {
		t.Fatal("a two-method double satisfies Bus — the interface is not frozen at what it declares")
	}
}

// partialBus implements two of Bus's three methods.
type partialBus struct{}

// Subscribe is the first half of the partial double.
func (*partialBus) Subscribe(corev.EventType, corev.SubscriptionValue) error { return nil }

// Unsubscribe is the second half of the partial double.
func (*partialBus) Unsubscribe(corev.EventType, string) error { return nil }

// TestTheZeroSubscriptionIsNotRunnable pins the shape the engine refuses. The
// value type must not acquire a usable zero: a subscription with no name and
// no listener is what a forgotten field looks like, and accepting it would
// register a listener that can never be reported on and never fires.
func TestTheZeroSubscriptionIsNotRunnable(t *testing.T) {
	t.Parallel()
	var zero corev.SubscriptionValue
	if zero.Name != "" || zero.Listener != nil || zero.MayHalt {
		t.Fatalf("the zero SubscriptionValue acquired content: %+v", zero)
	}
}

// TestTheZeroPriorityIsNormal — the ordering knob's zero value is a working
// default rather than a refusal, which is ADR 0031's first branch: the SDK
// can supply "no opinion" without inventing the caller's requirement.
func TestTheZeroPriorityIsNormal(t *testing.T) {
	t.Parallel()
	var zero corev.Priority
	if zero != corev.PriorityNormal {
		t.Fatalf("the zero Priority is not PriorityNormal: %d", zero)
	}
}

// TestTheZeroDispatchIsAnEmptyDispatch — a Publish into a bus nobody listens
// to returns this, and it must read as "nothing happened" rather than as a
// halt or a failure.
func TestTheZeroDispatchIsAnEmptyDispatch(t *testing.T) {
	t.Parallel()
	var zero corev.DispatchValue
	if zero.Delivered != 0 || zero.Failed != 0 || zero.Halted || zero.HaltedBy != "" || zero.Skipped != 0 {
		t.Fatalf("the zero DispatchValue is not empty: %+v", zero)
	}
}

// TestSentinelsCarryTheirDeclaredCodes keeps the errors.go / codes.go pair
// from drifting, which the AST audits check across the tree but not per-var.
func TestSentinelsCarryTheirDeclaredCodes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		sentinel *kerrs.Error
		code     kerrs.Code
	}{
		{"InvalidSubscription", corev.InvalidSubscription, corev.CodeInvalidSubscription},
		{"DuplicateListener", corev.DuplicateListener, corev.CodeDuplicateListener},
		{"UnknownListener", corev.UnknownListener, corev.CodeUnknownListener},
		{"InvalidEventType", corev.InvalidEventType, corev.CodeInvalidEventType},
		{"ListenerPanicked", corev.ListenerPanicked, corev.CodeListenerPanicked},
		{"Halt", corev.Halt, corev.CodeHalt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.Code(); got != tc.code {
				t.Fatalf("%s code: got %s, want %s", tc.name, got, tc.code)
			}
		})
	}
}

// TestHaltIsMatchableThroughAWrap — the control sentinel has to survive a
// listener adding context to it, or the mechanism would silently degrade to
// "returns the bare var" and every wrapper would become an ordinary failure.
func TestHaltIsMatchableThroughAWrap(t *testing.T) {
	t.Parallel()
	wrapped := kerrs.Wrap(corev.Halt, kerrs.WrapParams{}, kerrs.String("why", "already applied"))
	if !kerrs.HasCode(wrapped, corev.CodeHalt) {
		t.Fatalf("a wrapped Halt is no longer a Halt: %v", wrapped)
	}
}

// TestRegistrationRefusalsAreConfigurationFailures — EX_CONFIG (78), because
// the same Subscribe will be refused identically forever and retrying it is
// pointless. ListenerPanicked deliberately keeps EX_SOFTWARE: it is a fault
// in the listener, not in the wiring.
func TestRegistrationRefusalsAreConfigurationFailures(t *testing.T) {
	t.Parallel()
	const exitConfig, exitSoftware int = 78, 70
	for _, tc := range []struct {
		name     string
		sentinel *kerrs.Error
		exit     int
	}{
		{"InvalidSubscription", corev.InvalidSubscription, exitConfig},
		{"DuplicateListener", corev.DuplicateListener, exitConfig},
		{"UnknownListener", corev.UnknownListener, exitConfig},
		{"InvalidEventType", corev.InvalidEventType, exitConfig},
		{"ListenerPanicked", corev.ListenerPanicked, exitSoftware},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.sentinel.ExitCode(); got != tc.exit {
				t.Fatalf("%s exit code: got %d, want %d", tc.name, got, tc.exit)
			}
		})
	}
}

// TestAListenerValueIsCallable is a shape check with a purpose: the port is a
// func, so a plain function literal IS an implementation and no adapter type
// is needed at any call site.
func TestAListenerValueIsCallable(t *testing.T) {
	t.Parallel()
	//: the conversion is the assertion — it is what proves a bare func literal
	//: satisfies the port. Written as a conversion rather than `var x T = …` so
	//: KTN-VAR-SHORTDECL is satisfied without losing the type that is the point.
	listener := corev.Listener(func(_ context.Context, event any) error {
		if _, ok := event.(int); !ok {
			t.Errorf("listener saw %T", event)
		}
		return nil
	})
	if err := listener(context.Background(), 1); err != nil {
		t.Fatalf("listener: %v", err)
	}
}
