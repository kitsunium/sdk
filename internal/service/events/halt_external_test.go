package events_test

import (
	"context"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcev "github.com/kitsunium/sdk/internal/service/events"
)

// halting builds a Handler that records its name and returns Halt.
func halting(rec *recorder, name string, priority corev.Priority, mayHalt bool) svcev.Handler[orderPlaced] {
	return svcev.Handler[orderPlaced]{
		Name:     name,
		Priority: priority,
		MayHalt:  mayHalt,
		Handle: func(_ context.Context, _ orderPlaced) error {
			rec.note(name)
			return corev.Halt
		},
	}
}

// TestAnAuthorisedHaltStopsTheListenersAfterIt is the propagation-stop
// promise, asserted on the trace: the two listeners after the vetoing one
// never ran.
func TestAnAuthorisedHaltStopsTheListenersAfterIt(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus,
		rec.handler("first", -10, nil),
		halting(rec, "guard", 0, true),
		rec.handler("third", 10, nil),
		rec.handler("fourth", 20, nil))

	report := publish(t, bus, orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"first", "guard"})
	if !report.Halted || report.HaltedBy != "guard" {
		t.Fatalf("report: got %+v, want Halted by guard", report)
	}
	if report.Skipped != 2 {
		t.Fatalf("Skipped: got %d, want 2", report.Skipped)
	}
	if report.Delivered != 2 {
		t.Fatalf("Delivered: got %d, want 2", report.Delivered)
	}
}

// TestAHaltIsNotAFailure — publish returns a nil error. A halt is a decision
// the wiring asked for, and reporting it as an error would make every caller
// who uses the mechanism write a special case to ignore their own design.
func TestAHaltIsNotAFailure(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, halting(rec, "guard", 0, true), rec.handler("after", 1, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})
	if err != nil {
		t.Fatalf("Publish: a halt must not be an error, got %v", err)
	}
	if report.Failed != 0 {
		t.Fatalf("Failed: got %d, want 0", report.Failed)
	}
}

// TestAHaltWithoutTheAuthorityDoesNotStopAnything is the other half of the
// permission. Honouring it anyway would make MayHalt decorative; swallowing
// it silently would leave a listener convinced it had vetoed an event every
// one of its siblings then saw.
func TestAHaltWithoutTheAuthorityDoesNotStopAnything(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus,
		rec.handler("first", -10, nil),
		halting(rec, "overreach", 0, false),
		rec.handler("third", 10, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"first", "overreach", "third"})
	if report.Halted {
		t.Fatalf("report: an unauthorised halt stopped the dispatch: %+v", report)
	}
	if !kerrs.HasCode(err, svcev.CodeHaltNotPermitted) {
		t.Fatalf("HasCode(CodeHaltNotPermitted): got false for %v", err)
	}
	if report.Failed != 1 || report.Delivered != 3 {
		t.Fatalf("report: got %+v, want Delivered 3 / Failed 1", report)
	}
}

// TestAHaltCarriedByAWrappedErrorIsStillAHalt — a listener that adds context
// to the sentinel has still asked to stop. HasCode walks the chain, so the
// mechanism survives an errs.Wrap at the listener's own call site.
func TestAHaltCarriedByAWrappedErrorIsStillAHalt(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	wrapped := svcev.Handler[orderPlaced]{
		Name:    "guard",
		MayHalt: true,
		Handle: func(_ context.Context, _ orderPlaced) error {
			rec.note("guard")
			return kerrs.Wrap(corev.Halt, kerrs.WrapParams{}, kerrs.String("why", "already applied"))
		},
	}
	if err := svcev.On(bus, wrapped); err != nil {
		t.Fatalf("On: %v", err)
	}
	on(t, bus, rec.handler("after", 1, nil))

	report := publish(t, bus, orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"guard"})
	if !report.Halted || report.Skipped != 1 {
		t.Fatalf("report: got %+v, want Halted with 1 skipped", report)
	}
}

// TestAPanicIsNeverReadAsAHalt — even from a listener that HOLDS the
// authority. A crash is not a decision, and a listener that panicked chose
// nothing; reading it as a veto would let one broken observer silence every
// listener after it.
func TestAPanicIsNeverReadAsAHalt(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	exploding := svcev.Handler[orderPlaced]{
		Name:    "authorised-but-broken",
		MayHalt: true,
		Handle: func(_ context.Context, _ orderPlaced) error {
			rec.note("authorised-but-broken")
			panic(corev.Halt)
		},
	}
	if err := svcev.On(bus, exploding); err != nil {
		t.Fatalf("On: %v", err)
	}
	on(t, bus, rec.handler("after", 1, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"authorised-but-broken", "after"})
	if report.Halted {
		t.Fatalf("a panic was read as a halt: %+v", report)
	}
	if !kerrs.HasCode(err, corev.CodeListenerPanicked) {
		t.Fatalf("HasCode(CodeListenerPanicked): got false for %v", err)
	}
}
