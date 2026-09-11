package events_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcev "github.com/kitsunium/sdk/internal/service/events"
)

// TestListenersRunInAscendingPriority is the domain's headline promise.
// Registration order here is deliberately the OPPOSITE of the priority order,
// so a bus that simply appended would fail.
func TestListenersRunInAscendingPriority(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus,
		rec.handler("last", 10, nil),
		rec.handler("normal", corev.PriorityNormal, nil),
		rec.handler("first", -10, nil))

	report := publish(t, bus, orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"first", "normal", "last"})
	if report.Delivered != 3 {
		t.Fatalf("Delivered: got %d, want 3", report.Delivered)
	}
}

// TestEqualPrioritiesRunInRegistrationOrder pins the tie rule. Eight
// listeners at one priority is enough that a map-iteration implementation
// would be caught on essentially every run, and the assertion is on the exact
// sequence rather than on membership.
func TestEqualPrioritiesRunInRegistrationOrder(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	want := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, name := range want {
		on(t, bus, rec.handler(name, corev.PriorityNormal, nil))
	}

	publish(t, bus, orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), want)
}

// TestTheOrderIsIdenticalAcrossRepeatedPublishes is the OTHER half of
// "deterministic": one dispatch being right proves nothing if the next one
// differs. Ten publishes over a mixed priority set must produce the same trace
// ten times.
func TestTheOrderIsIdenticalAcrossRepeatedPublishes(t *testing.T) {
	t.Parallel()
	bus := svcev.New()
	rec := newRecorder()
	on(t, bus,
		rec.handler("audit", -5, nil),
		rec.handler("cache", corev.PriorityNormal, nil),
		rec.handler("metric", corev.PriorityNormal, nil),
		rec.handler("archive", 5, nil))
	want := []string{"audit", "cache", "metric", "archive"}
	const rounds int = 10

	for round := range rounds {
		publish(t, bus, orderPlaced{id: round})
	}

	got := rec.snapshot()
	if len(got) != rounds*len(want) {
		t.Fatalf("trace length: got %d, want %d", len(got), rounds*len(want))
	}
	for round := range rounds {
		assertCalls(t, got[round*len(want):(round+1)*len(want)], want)
	}
}

// TestAnEmptyBusIsLegitimate — ADR 0031 and ADR 0053 §D6: a publisher must
// not have to know whether anybody is listening.
func TestAnEmptyBusIsLegitimate(t *testing.T) {
	t.Parallel()
	report, err := svcev.New().Publish(context.Background(), orderPlaced{id: 1})
	if err != nil {
		t.Fatalf("Publish on an empty bus: %v", err)
	}
	if report != (corev.DispatchValue{}) {
		t.Fatalf("report: got %+v, want the zero DispatchValue", report)
	}
}

// TestAnUnlistenedTypeIsAlsoLegitimate covers the second shape of "nobody is
// listening": the bus has listeners, just not for this type.
func TestAnUnlistenedTypeIsAlsoLegitimate(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("placed", corev.PriorityNormal, nil))

	report, err := bus.Publish(context.Background(), orderShipped{id: 1})
	if err != nil {
		t.Fatalf("Publish of an unlistened type: %v", err)
	}
	if report != (corev.DispatchValue{}) {
		t.Fatalf("report: got %+v, want the zero DispatchValue", report)
	}
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("a listener for another type ran: %v", got)
	}
}

// TestTwoEventTypesNeverCross is what keying on the Go type buys: the
// compiler mints the key, so "user.created" cannot be spelled twice.
func TestTwoEventTypesNeverCross(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("placed", corev.PriorityNormal, nil))
	shipped := svcev.Handler[orderShipped]{
		Name:   "shipped",
		Handle: func(_ context.Context, _ orderShipped) error { rec.note("shipped"); return nil },
	}
	if err := svcev.On(bus, shipped); err != nil {
		t.Fatalf("On(shipped): %v", err)
	}

	publish(t, bus, orderPlaced{id: 1})
	if _, err := bus.Publish(context.Background(), orderShipped{id: 2}); err != nil {
		t.Fatalf("Publish(shipped): %v", err)
	}

	assertCalls(t, rec.snapshot(), []string{"placed", "shipped"})
}

// TestAListenerErrorDoesNotStopTheDispatch is the error policy, asserted on
// the observable trace: the listeners after the failing one still ran.
func TestAListenerErrorDoesNotStopTheDispatch(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus,
		rec.handler("first", -1, nil),
		rec.handler("broken", corev.PriorityNormal, errDisk),
		rec.handler("last", 1, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"first", "broken", "last"})
	if err == nil {
		t.Fatal("Publish: expected the listener's failure to be reported")
	}
	if report.Delivered != 3 || report.Failed != 1 {
		t.Fatalf("report: got %+v, want Delivered 3 / Failed 1", report)
	}
}

// TestAFailureCarriesBothTheVerdictAndTheCause is why the aggregate is an
// errors.Join and not an errs.Wrap: origin-wins would have relabelled the
// SDK's verdict with the listener's own error.
func TestAFailureCarriesBothTheVerdictAndTheCause(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus, rec.handler("broken", corev.PriorityNormal, errDisk))

	_, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	if !kerrs.HasCode(err, svcev.CodeListenerFailed) {
		t.Fatalf("HasCode(CodeListenerFailed): got false for %v", err)
	}
	if !errors.Is(err, errDisk) {
		t.Fatalf("errors.Is(errDisk): got false for %v", err)
	}
}

// TestEveryFailingListenerIsReported checks that the aggregate collects them
// all rather than keeping the first.
func TestEveryFailingListenerIsReported(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	other := errors.New("network down")
	on(t, bus,
		rec.handler("one", 1, errDisk),
		rec.handler("two", 2, other),
		rec.handler("three", 3, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	if report.Failed != 2 || report.Delivered != 3 {
		t.Fatalf("report: got %+v, want Delivered 3 / Failed 2", report)
	}
	if !errors.Is(err, errDisk) || !errors.Is(err, other) {
		t.Fatalf("both causes must survive the aggregate: %v", err)
	}
}

// TestPublishingAnUntypedNilIsRefused — a nil has no dynamic type, so no
// subscription could ever have been keyed on it. Delivering to nobody would
// be indistinguishable from a bus wired wrong.
func TestPublishingAnUntypedNilIsRefused(t *testing.T) {
	t.Parallel()
	_, err := svcev.New().Publish(context.Background(), nil)
	if !kerrs.HasCode(err, corev.CodeInvalidEventType) {
		t.Fatalf("Publish(nil): got %v, want INVALID_EVENT_TYPE", err)
	}
}

// TestAPanicInAListenerReachesNeitherThePublisherNorTheSiblings is decision
// D5: the publisher survives, the listeners after the broken one still run,
// and the fault is reported rather than swallowed.
func TestAPanicInAListenerReachesNeitherThePublisherNorTheSiblings(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	bus := svcev.New()
	on(t, bus,
		rec.handler("first", -1, nil),
		svcev.Handler[orderPlaced]{
			Name: "exploding",
			Handle: func(_ context.Context, _ orderPlaced) error {
				rec.note("exploding")
				panic("listener blew up")
			},
		},
		rec.handler("last", 1, nil))

	report, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	assertCalls(t, rec.snapshot(), []string{"first", "exploding", "last"})
	if !kerrs.HasCode(err, corev.CodeListenerPanicked) {
		t.Fatalf("HasCode(CodeListenerPanicked): got false for %v", err)
	}
	//: a listener that crashed did not deliver; the two that returned did.
	if report.Delivered != 2 || report.Failed != 1 {
		t.Fatalf("report: got %+v, want Delivered 2 / Failed 1", report)
	}
}

// TestAPanicCarriesTheOriginatingStackAndValue — a recovered panic whose
// stack is gone is a crash report naming the recover site, which is code that
// did nothing wrong. kernel/group makes the same promise across a goroutine
// boundary; here there is none, so the stack is simply captured in place.
func TestAPanicCarriesTheOriginatingStackAndValue(t *testing.T) {
	t.Parallel()
	bus := svcev.New()
	if err := svcev.On(bus, svcev.Handler[orderPlaced]{
		Name:   "exploding",
		Handle: func(_ context.Context, _ orderPlaced) error { panic("boom") },
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	_, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	fields := fieldMap(t, err, corev.CodeListenerPanicked)
	if fields["panic"] != "boom" {
		t.Fatalf("panic field: got %q, want %q", fields["panic"], "boom")
	}
	//: the claim is that the stack points at the code that PANICKED, not at the
	//: recover site — so it is asserted on the panicking frame's own file. The
	//: package-qualified name was tried first and is wrong: `go test` spells the
	//: external test package `events_test` while Bazel spells it
	//: `events_test_test`, so an assertion on either passes under one build
	//: system and fails under the other while the behaviour is identical.
	if !strings.Contains(fields["stack"], "dispatch_external_test.go") {
		t.Fatalf("stack field does not name the panicking frame: %q", fields["stack"])
	}
	//: and it must not be merely the recover site, which is what a lost stack
	//: degrades to.
	if !strings.Contains(fields["stack"], "publish.go") {
		t.Fatalf("stack field lost the dispatch frames: %q", fields["stack"])
	}
	if fields["listener"] != "exploding" {
		t.Fatalf("listener field: got %q", fields["listener"])
	}
}

// TestAPanicCarryingAnSDKErrorCannotHijackTheCode — errs.Wrap's origin-wins
// rule means a panic(someSentinel) used as the WRAP CAUSE would relabel the
// bus's own verdict with the attacker's code. The value travels as a field
// precisely so it cannot.
func TestAPanicCarryingAnSDKErrorCannotHijackTheCode(t *testing.T) {
	t.Parallel()
	bus := svcev.New()
	if err := svcev.On(bus, svcev.Handler[orderPlaced]{
		Name:   "impostor",
		Handle: func(_ context.Context, _ orderPlaced) error { panic(corev.DuplicateListener) },
	}); err != nil {
		t.Fatalf("On: %v", err)
	}

	_, err := bus.Publish(context.Background(), orderPlaced{id: 1})

	if !kerrs.HasCode(err, corev.CodeListenerPanicked) {
		t.Fatalf("the panic value hijacked the code: %v", err)
	}
	if kerrs.HasCode(err, corev.CodeDuplicateListener) {
		t.Fatalf("the panic value reached the code chain: %v", err)
	}
}

// fieldMap digs the *errs.Error carrying code out of an aggregate and returns
// its fields as a map.
func fieldMap(t *testing.T, err error, code kerrs.Code) map[string]string {
	t.Helper()
	var found *kerrs.Error
	var walk func(error)
	walk = func(e error) {
		if e == nil || found != nil {
			return
		}
		var typed *kerrs.Error
		if errors.As(e, &typed) && typed.Code() == code {
			found = typed
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
	if found == nil {
		t.Fatalf("no *errs.Error with code %s in %v", code, err)
	}
	fields := map[string]string{}
	for _, f := range found.Fields() {
		fields[f.Key()] = f.StringValue()
	}
	return fields
}
