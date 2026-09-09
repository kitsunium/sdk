package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// add registers every component, failing the test on the first refusal.
func add(t *testing.T, lc corelc.Lifecycle, components ...corelc.ComponentValue) {
	t.Helper()
	for _, component := range components {
		if err := lc.Add(component); err != nil {
			t.Fatalf("Add(%q): %v", component.Name, err)
		}
	}
}

// TestStartsInOrderAndStopsInReverse is the domain's headline promise, and the
// only one every other test builds on.
func TestStartsInOrderAndStopsInReverse(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), rec.ok("cache"), rec.ok("http"))
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertCalls(t, rec.snapshot(), []string{
		"start:db", "start:cache", "start:http",
		"stop:http", "stop:cache", "stop:db",
	})
}

// TestPartialStartUnwindsTheStartedPrefixInReverse is the case the whole
// domain exists for: the fourth component of six fails, and the three that are
// up must come back down — in reverse, before Start returns, and without the
// failing component or the two never reached being touched at all.
//
// A hand-rolled `defer close()` per component gets this wrong in the two ways
// asserted here: it either stops the component that failed (a double close, on
// a half-constructed thing) or it stops nothing, because the deferred cleanup
// was registered after the error return.
func TestPartialStartUnwindsTheStartedPrefixInReverse(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc,
		rec.ok("one"), rec.ok("two"), rec.ok("three"),
		rec.failing("four", errDial),
		rec.ok("five"), rec.ok("six"))

	err := lc.Start(context.Background())
	if err == nil {
		t.Fatalf("Start succeeded despite a failing component")
	}
	assertCalls(t, rec.snapshot(), []string{
		"start:one", "start:two", "start:three", "start:four",
		//: exactly the started prefix, exactly reversed.
		"stop:three", "stop:two", "stop:one",
	})
	//: the aggregate answers both questions a caller can ask.
	assertHasCode(t, err, svclc.CodeStartFailed, "the SDK's own verdict")
	if !errors.Is(err, errDial) {
		t.Fatalf("the component's own error did not survive: %v", err)
	}
	//: a clean unwind adds no second verdict.
	if kerrs.HasCode(err, svclc.CodeUnwindFailed) {
		t.Fatalf("a clean unwind reported UNWIND_FAILED: %v", err)
	}
}

// TestUnwindRunsEvenWhenTheStartContextIsAlreadyCancelled pins the detail that
// makes the unwind worth having. A start most often fails BECAUSE its context
// was cancelled, so a cleanup driven by that same context would hand every
// Stop a context that is already dead — and a well-behaved Stop, seeing it,
// would abandon the very teardown the unwind exists to perform.
func TestUnwindRunsEvenWhenTheStartContextIsAlreadyCancelled(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), corelc.ComponentValue{
		Name: "http",
		Start: func(inner context.Context) error {
			rec.note("start:http")
			cancel()
			//: the shape of every real deadline-aborted startup.
			return inner.Err()
		},
		Stop: func(_ context.Context) error {
			rec.note("stop:http")
			return nil
		},
	})

	err := lc.Start(ctx)
	if err == nil {
		t.Fatalf("Start succeeded on a cancelled context")
	}
	assertCalls(t, rec.snapshot(), []string{"start:db", "start:http", "stop:db"})
	//: and the cleanup was given a LIVE context, not the dead one that caused it.
	if !rec.stopCtxLive["db"] {
		t.Fatalf("db.Stop was handed an already-cancelled context during the unwind")
	}
}

// TestAPanickingStartIsRecoveredAndStillUnwinds closes the hole a recover in
// the caller's main cannot: a panic escaping a Start skips the unwind
// entirely, so every component already up leaks — the exact failure this
// domain claims to prevent, on the one path where nobody looks.
func TestAPanickingStartIsRecoveredAndStillUnwinds(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), corelc.ComponentValue{
		Name:  "http",
		Start: func(_ context.Context) error { panic("listener: address already in use") },
		Stop:  func(_ context.Context) error { rec.note("stop:http"); return nil },
	})

	err := lc.Start(context.Background())
	if err == nil {
		t.Fatalf("Start succeeded despite a panicking component")
	}
	assertCalls(t, rec.snapshot(), []string{"start:db", "stop:db"})
	assertHasCode(t, err, corelc.CodeComponentPanicked, "a recovered panic")
	assertHasCode(t, err, svclc.CodeStartFailed, "the start verdict")
}

// TestABrokenUnwindNeverHidesTheStartFailure. A teardown that also fails is a
// SECOND defect. Reporting only it — the natural outcome of overwriting the
// error variable in a cleanup loop — sends the operator to debug the wrong
// component.
func TestABrokenUnwindNeverHidesTheStartFailure(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	cache := rec.ok("cache")
	cache.Stop = func(_ context.Context) error { rec.note("stop:cache"); return errFlushShort }
	add(t, lc, cache, rec.failing("db", errDirtySchema))

	err := lc.Start(context.Background())
	if err == nil {
		t.Fatalf("Start succeeded despite a failing component")
	}
	assertHasCode(t, err, svclc.CodeStartFailed, "the start verdict")
	assertHasCode(t, err, svclc.CodeUnwindFailed, "the unwind verdict")
	assertHasCode(t, err, svclc.CodeStopFailed, "the component's stop verdict")
	//: and BOTH original errors survive, so neither investigation is lost.
	if !errors.Is(err, errDirtySchema) || !errors.Is(err, errFlushShort) {
		t.Fatalf("an original error was swallowed: %v", err)
	}
}

// TestTheUnwindAndAnOrdinaryStopProduceTheSameOrder is the executable form of
// "there is one unwind path". Two routes reach it — a failed start and an
// explicit Stop — and a second implementation for the failure path is exactly
// how the two drift until only the one people exercise stays correct.
func TestTheUnwindAndAnOrdinaryStopProduceTheSameOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		abort bool
	}{
		{"an ordinary Stop", false},
		{"the cleanup after a partial start", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder()
			lc := svclc.New(svclc.Config{Clock: manual()})
			add(t, lc, rec.ok("one"), rec.ok("two"), rec.ok("three"))
			//: the abort route appends a component that cannot start, so the
			//: three above are exactly the started prefix in both routes.
			if tc.abort {
				add(t, lc, rec.failing("four", errDial))
			}
			startErr := lc.Start(context.Background())
			//: the abort route is SUPPOSED to fail; the ordinary one is not.
			if (startErr != nil) != tc.abort {
				t.Fatalf("Start returned %v on the %q route", startErr, tc.name)
			}
			//: on the ordinary route the Stop is explicit; on the abort route
			//: it already happened and this call is the documented no-op.
			if stopErr := lc.Stop(context.Background()); stopErr != nil {
				t.Fatalf("Stop: %v", stopErr)
			}
			got := rec.snapshot()
			assertCalls(t, got[len(got)-3:], []string{"stop:three", "stop:two", "stop:one"})
		})
	}
}

// TestALifecycleIsUsableAgainAfterAFailedStart. Nothing is up once the unwind
// has run, so refusing every later Add and Start would leave the caller with
// an object that reports a state it is not in.
func TestALifecycleIsUsableAgainAfterAFailedStart(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	attempts := 0
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), corelc.ComponentValue{
		Name: "http",
		Start: func(_ context.Context) error {
			attempts++
			rec.note("start:http")
			//: fails once, then succeeds — a port that was still in TIME_WAIT.
			if attempts == 1 {
				return errAddrInUse
			}
			return nil
		},
		Stop: func(_ context.Context) error { rec.note("stop:http"); return nil },
	})

	if err := lc.Start(context.Background()); err == nil {
		t.Fatalf("the first Start should have failed")
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("the second Start: %v", err)
	}
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertCalls(t, rec.snapshot(), []string{
		"start:db", "start:http", "stop:db",
		"start:db", "start:http", "stop:http", "stop:db",
	})
}

// TestASecondStartIsRefused. Two concurrent Starts would bring every
// component up twice, and the second set would never be recorded as started
// — so it would never be stopped either.
func TestASecondStartIsRefused(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"))
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	err := lc.Start(context.Background())
	assertHasCode(t, err, corelc.CodeLifecycleRunning, "a second Start")
	assertCalls(t, rec.snapshot(), []string{"start:db"})
}
