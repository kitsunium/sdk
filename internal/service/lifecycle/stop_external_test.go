package lifecycle_test

import (
	"context"
	"testing"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/lifecycle"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
)

// budget is the per-component stop budget every timing case here configures.
// It is a round number so a failure message reads clearly; nothing waits it
// out, the ManualClock is simply moved past it.
const budget time.Duration = 5 * time.Second

// TestAnExpiredBudgetAbandonsTheComponentAndKeepsGoing is the ADR 0043 lesson
// made executable. The middle component never returns; the two around it must
// still be stopped, the overrun must be NAMED, and the abandoned component
// must be told — by a cancelled context and by nothing else.
func TestAnExpiredBudgetAbandonsTheComponentAndKeepsGoing(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	clk := manual()
	entered, release := make(chan struct{}), make(chan struct{})
	stuck, sawCancel, finished := rec.blocking("cache", entered, release)
	lc := svclc.New(svclc.Config{Clock: clk, StopTimeout: budget})
	add(t, lc, rec.ok("db"), stuck, rec.ok("http"))
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := stopAsync(lc)
	//: rendezvous, not a sleep: once cache's Stop has been entered, http's
	//: Stop has returned and its timer has been retired, so the only armed
	//: wait on the clock is cache's own budget.
	<-entered
	clk.Advance(budget)

	err := <-stopped
	assertHasCode(t, err, svclc.CodeStopTimeout, "an overrun component")
	//: db was stopped AFTER cache was abandoned — the shutdown continued.
	assertCalls(t, rec.snapshot(), []string{
		"start:db", "start:cache", "start:http", "stop:http", "stop:db",
	})

	//: nothing was severed: the abandoned Stop is still running and still
	//: able to finish. Release it and watch it do exactly that.
	close(release)
	<-finished
	if !*sawCancel {
		t.Fatalf("the abandoned Stop was never told its budget had expired")
	}
}

// TestEachComponentGetsItsOwnBudget is why the budget is per-component and
// not one budget for the shutdown. With a shared one, the first component
// that will not finish spends everything and every component after it is
// severed without ever being asked — which is the defect ADR 0043 removed
// from the HTTP drain, reintroduced one layer up.
func TestEachComponentGetsItsOwnBudget(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	clk := manual()
	enteredCache, releaseCache := make(chan struct{}), make(chan struct{})
	enteredHTTP, releaseHTTP := make(chan struct{}), make(chan struct{})
	stuckCache, _, _ := rec.blocking("cache", enteredCache, releaseCache)
	stuckHTTP, _, _ := rec.blocking("http", enteredHTTP, releaseHTTP)
	lc := svclc.New(svclc.Config{Clock: clk, StopTimeout: budget})
	add(t, lc, rec.ok("db"), stuckCache, stuckHTTP)
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := stopAsync(lc)
	//: http is stopped first and burns a whole budget.
	<-enteredHTTP
	clk.Advance(budget)
	//: cache is next and gets a WHOLE budget of its own, not the remainder.
	<-enteredCache
	clk.Advance(budget)

	err := <-stopped
	assertHasCode(t, err, svclc.CodeStopTimeout, "two overrun components")
	//: db is the proof: a shared budget was exhausted twice over by now, and
	//: db would never have been asked.
	assertCalls(t, rec.snapshot(), []string{"start:db", "start:cache", "start:http", "stop:db"})
	close(releaseHTTP)
	close(releaseCache)
}

// TestANonPositiveBudgetClampsRatherThanStoppingAtOnce. A zero here is what an
// unset field looks like, and reading it as "give every component no time at
// all" turns a forgotten line into a shutdown that reports STOP_TIMEOUT for
// components that were about to succeed (ADR 0031).
//
// The assertion is OBSERVABLE — what the shutdown did — not the field value,
// so it survives a change of mechanism.
func TestANonPositiveBudgetClampsRatherThanStoppingAtOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		configure time.Duration
	}{
		{"an unset budget", 0},
		{"a negative budget", -time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := newRecorder()
			clk := manual()
			entered, release := make(chan struct{}), make(chan struct{})
			stuck, _, finished := rec.blocking("db", entered, release)
			lc := svclc.New(svclc.Config{Clock: clk, StopTimeout: tc.configure})
			add(t, lc, stuck)
			if err := lc.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}

			stopped := stopAsync(lc)
			<-entered
			//: one nanosecond short of the documented clamp. A budget read as
			//: "stop at once" would already have fired — clock.ManualClock
			//: delivers a non-positive timer immediately, by contract.
			clk.Advance(svclc.DefaultStopTimeout - time.Nanosecond)
			close(release)
			<-finished

			if err := <-stopped; err != nil {
				t.Fatalf("a component that finished inside the clamped budget reported %v", err)
			}
			assertCalls(t, rec.snapshot(), []string{"start:db", "stop:db"})
		})
	}
}

// TestTheClampedBudgetIsTheDocumentedOne pins the number the previous test
// only bounds from below. Advancing by exactly DefaultStopTimeout must expire
// it — otherwise "clamps to 30s" could quietly become "clamps to an hour".
func TestTheClampedBudgetIsTheDocumentedOne(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	clk := manual()
	entered, release := make(chan struct{}), make(chan struct{})
	stuck, _, _ := rec.blocking("db", entered, release)
	lc := svclc.New(svclc.Config{Clock: clk})
	add(t, lc, stuck)
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := stopAsync(lc)
	<-entered
	clk.Advance(svclc.DefaultStopTimeout)
	assertHasCode(t, <-stopped, svclc.CodeStopTimeout, "the clamped budget")
	close(release)
}

// TestStopHonoursAnAlreadyCancelledContext. The context that told a process to
// shut down is, almost always, one that has just been cancelled. A Stop that
// respected it would make every real shutdown a no-op, and would hand each
// component a context that is dead before it has closed anything.
func TestStopHonoursAnAlreadyCancelledContext(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), rec.ok("http"))
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := lc.Stop(ctx); err != nil {
		t.Fatalf("Stop on a cancelled context: %v", err)
	}
	assertCalls(t, rec.snapshot(), []string{"start:db", "start:http", "stop:http", "stop:db"})
	for _, name := range []string{"db", "http"} {
		if !rec.stopCtxLive[name] {
			t.Fatalf("%s.Stop was handed the caller's already-cancelled context", name)
		}
	}
}

// TestStopIsANoOpBeforeStartAndIsIdempotent. `defer lc.Stop(ctx)` is the
// shape every caller writes, and it runs on the path where Start failed and
// on the path where Stop already ran. Both must be silent.
func TestStopIsANoOpBeforeStartAndIsIdempotent(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"))
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range 3 {
		if err := lc.Stop(context.Background()); err != nil {
			t.Fatalf("repeated Stop: %v", err)
		}
	}
	//: exactly one stop:db — a component is never taken down twice.
	assertCalls(t, rec.snapshot(), []string{"start:db", "stop:db"})
}

// TestAPanickingStopDoesNotAbandonTheRest. A panic escaping one Stop would
// unwind past the loop and leave every component before it in the order
// running — during shutdown, which is the one moment a caller cannot rehearse.
func TestAPanickingStopDoesNotAbandonTheRest(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"), corelc.ComponentValue{
		Name:  "http",
		Start: func(_ context.Context) error { rec.note("start:http"); return nil },
		Stop:  func(_ context.Context) error { panic("closing a nil listener") },
	})
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	err := lc.Stop(context.Background())
	assertHasCode(t, err, corelc.CodeComponentPanicked, "a panicking Stop")
	assertHasCode(t, err, svclc.CodeStopFailed, "the stop verdict")
	//: db was still taken down.
	assertCalls(t, rec.snapshot(), []string{"start:db", "start:http", "stop:db"})
}

// TestTransitionsReportEveryDecisionIncludingTheAbandonedOne. A hook that only
// saw the calls that returned would make a component which never stops look
// exactly like one that stops instantly — and would leave "this component
// overran" provable only by timing the process.
func TestTransitionsReportEveryDecisionIncludingTheAbandonedOne(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	clk := manual()
	entered, release := make(chan struct{}), make(chan struct{})
	stuck, _, _ := rec.blocking("cache", entered, release)
	var seen []corelc.TransitionValue
	lc := svclc.New(svclc.Config{
		Clock:        clk,
		StopTimeout:  budget,
		OnTransition: func(tr corelc.TransitionValue) { seen = append(seen, tr) },
	})
	add(t, lc, rec.ok("db"), stuck)
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stopped := stopAsync(lc)
	<-entered
	clk.Advance(budget)
	<-stopped
	close(release)

	//: start:db, start:cache, stop:cache (abandoned), stop:db.
	if len(seen) != 4 {
		t.Fatalf("saw %d transitions, want 4: %+v", len(seen), seen)
	}
	abandoned := seen[2]
	if abandoned.Name != "cache" || abandoned.Phase != corelc.PhaseStop || !abandoned.TimedOut {
		t.Fatalf("the abandoned transition reads %+v", abandoned)
	}
	if !kerrs.HasCode(abandoned.Err, svclc.CodeStopTimeout) {
		t.Fatalf("the abandoned transition carries %v, want STOP_TIMEOUT", abandoned.Err)
	}
	//: Ended is when the budget expired, which the ManualClock puts exactly
	//: one budget past Begun — not when the component eventually returned.
	if got := abandoned.Ended.Sub(abandoned.Begun); got != budget {
		t.Fatalf("abandoned transition spanned %v, want the budget %v", got, budget)
	}
	if seen[3].Name != "db" || seen[3].TimedOut {
		t.Fatalf("db's transition reads %+v", seen[3])
	}
}

// TestAddRefusesWhatCouldNeverRun. Every refusal here is permanent: the same
// Add will be refused identically forever, which is what EX_CONFIG says and
// what the core test pins.
func TestAddRefusesWhatCouldNeverRun(t *testing.T) {
	t.Parallel()
	noop := func(_ context.Context) error { return nil }
	tests := []struct {
		name      string
		component corelc.ComponentValue
		code      kerrs.Code
	}{
		{"an empty name", corelc.ComponentValue{Start: noop, Stop: noop}, corelc.CodeInvalidComponent},
		{"a nil Start", corelc.ComponentValue{Name: "db", Stop: noop}, corelc.CodeInvalidComponent},
		{"a nil Stop", corelc.ComponentValue{Name: "db", Start: noop}, corelc.CodeInvalidComponent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lc := svclc.New(svclc.Config{Clock: manual()})
			assertHasCode(t, lc.Add(tc.component), tc.code, tc.name)
		})
	}
}

// TestAddRefusesADuplicateNameAndARunningLifecycle. A duplicate name makes
// every transition and every error ambiguous; a mid-flight Add gives the new
// component a start position it never had and no defensible place in the
// reverse order.
func TestAddRefusesADuplicateNameAndARunningLifecycle(t *testing.T) {
	t.Parallel()
	rec := newRecorder()
	lc := svclc.New(svclc.Config{Clock: manual()})
	add(t, lc, rec.ok("db"))
	assertHasCode(t, lc.Add(rec.ok("db")), corelc.CodeDuplicateComponent, "a duplicate name")
	if err := lc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	assertHasCode(t, lc.Add(rec.ok("http")), corelc.CodeLifecycleRunning, "an Add mid-flight")
	if err := lc.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	//: and it is allowed again once the lifecycle has stopped.
	if err := lc.Add(rec.ok("http")); err != nil {
		t.Fatalf("Add after Stop: %v", err)
	}
}
