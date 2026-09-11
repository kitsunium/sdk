// Package health_test — the per-check budget, what expiring it does and does
// not do, and the cache.
package health_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svchealth "github.com/kitsunium/sdk/internal/service/health"
)

// TestABudgetExpiryIsAFailureAndSaysSo pins the answer to "what does a timeout
// mean as a result".
//
// It is a FAILURE — a probe that could not answer in time is operationally a
// no for whoever must decide about routing — and it is a DISTINGUISHABLE one:
// TimedOut separates "the dependency said no" from "the dependency said
// nothing". An "unknown" that aggregated as healthy would make a wedged
// dependency invisible, which is the exact failure probes exist to catch.
func TestABudgetExpiryIsAFailureAndSaysSo(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	release, cancelled := make(chan struct{}), make(chan struct{})
	defer close(release)
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: blocking(release, cancelled),
	})
	//: two timers: the probe's, and the run's own budget.
	report := probeUnderClock(t, registry, clk, corehealth.ProbeReadiness, 2)
	result := resultFor(t, report, "db")
	if result.Status != corehealth.StatusUnhealthy {
		t.Errorf("a timed-out critical check = %v, want unhealthy", result.Status)
	}
	if !result.TimedOut {
		t.Error("the result is not marked TimedOut; a timeout must stay tellable " +
			"apart from an ordinary failure")
	}
	if !errs.HasCode(result.Err, svchealth.CodeCheckTimeout) {
		t.Errorf("the result carries %v, want CHECK_TIMEOUT", result.Err)
	}
	if result.Took != budget {
		t.Errorf("Took = %v, want the budget %v — the registry does not know "+
			"when the check eventually returned", result.Took, budget)
	}
	//: the announcement reached the body. It is an announcement and nothing
	//: more: the goroutine is not killed and nothing it holds is closed.
	<-cancelled
}

// TestACancelledCallerStopsWaitingAndLeavesTheRunAlone pins both halves of a
// caller's departure.
//
// A probe used to wait only for its run or its budget, so a caller whose own
// context ended — a lifecycle startup interrupted by SIGTERM, a client that
// hung up — sat out the whole budget for an answer nobody would read. It must
// return at once. And it must return WITHOUT cancelling the run: that run is
// shared by every probe waiting on it, so the next probe joins it rather than
// starting a second body, and a wedged dependency keeps costing one goroutine.
//
// "At once" is asserted without a clock: inside a synctest bubble,
// synctest.Wait returns when every other goroutine is durably blocked, so a
// probe still parked on its budget is SEEN still parked — the manual clock is
// never advanced, which is how "promptly" and "not at the budget" become the
// same assertion.
//
// MUTATION (2026-09-11): the `case <-ctx.Done()` arm was deleted. Observed:
// `the cancelled probe is still waiting — it would have sat out its 2s budget
// for a caller that has gone`. Restored; SHA-256 of runner.go identical to the
// pre-mutation file.
//
// MUTATION (2026-09-11): the ctx.Done arm made to cancel the shared run before
// departing (`run.cancel()`, as abandoned does for an expired budget).
// Observed: `the caller's departure cancelled the shared run: context
// canceled`. Restored; SHA-256 identical.
func TestACancelledCallerStopsWaitingAndLeavesTheRunAlone(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		registry, clk := newRegistry(t, svchealth.Config{})
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		//: whatever happens below, the body is let go before the bubble ends.
		defer releaseOnce()
		runs := make(chan context.Context, 2)
		var calls atomic.Int64
		mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
			Name: "db", Check: func(ctx context.Context) error {
				calls.Add(1)
				runs <- ctx
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					return nil
				}
			},
		})
		callerCtx, cancel := context.WithCancel(context.Background())
		reports := make(chan corehealth.ReportValue, 2)
		go func() { reports <- registry.Probe(callerCtx, corehealth.ProbeReadiness) }()
		runCtx := <-runs
		//: the probe is parked on its budget before its caller leaves. Two
		//: timers: the probe's own, and the run's — which is the whole point
		//: here, since it is what will still expire once this caller is gone.
		clk.BlockUntil(2)
		cancel()
		synctest.Wait()
		var first corehealth.ReportValue
		select {
		case first = <-reports:
		default:
			t.Fatalf("the cancelled probe is still waiting — it would have sat out its %v budget for a caller that has gone", budget)
		}
		result := resultFor(t, first, "db")
		if !errs.HasCode(result.Err, svchealth.CodeCheckTimeout) || !errors.Is(result.Err, context.Canceled) {
			t.Errorf("the departed probe reported %v, want CHECK_TIMEOUT caused by the caller's context", result.Err)
		}
		if !result.TimedOut || result.Took != 0 {
			t.Errorf("TimedOut = %v, Took = %v; want true and 0 — the clock never moved", result.TimedOut, result.Took)
		}
		//: the shared run heard nothing about one caller leaving.
		if err := runCtx.Err(); err != nil {
			t.Fatalf("the caller's departure cancelled the shared run: %v", err)
		}
		//: the next probe JOINS that run: parked on its own budget first, so
		//: the release below cannot reach a run nobody has joined yet. The
		//: run's timer is still armed — the clock never moved — while the
		//: departed probe's own was stopped on its way out, so two again.
		go func() { reports <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
		clk.BlockUntil(2)
		releaseOnce()
		second := resultFor(t, <-reports, "db")
		if second.Status != corehealth.StatusHealthy || second.Err != nil {
			t.Errorf("the joining probe got %v (%v), want the run's own healthy answer", second.Status, second.Err)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("%d check bodies ran, want 1 — the second probe started a run of its own", got)
		}
	})
}

// TestARunAbandonedByEveryCallerStillExpires is the other half of
// [TestACancelledCallerStopsWaitingAndLeavesTheRunAlone], and the defect that
// half had left open.
//
// A departing caller deliberately cancels nothing: its context is its own,
// while the run belongs to every probe waiting on it. But when the LAST caller
// leaves, the budget lived only on the waiting side — so a run nobody was
// waiting for was cancelled by nobody, and its body was never told its time was
// up. That is the ordinary shape of a polled endpoint behind a proxy with a
// shorter timeout of its own: every probe departs early, and a check that
// honours its context holds its dependency's connection for the whole outage
// while every probe reports a timeout.
//
// The budget now belongs to the RUN, so it expires whether or not anyone is
// left. The clock is the only thing that moves: no caller is waiting.
//
// Seen failing with health.boundRun removed: "panic: deadlock: all goroutines
// in bubble are blocked", raised at the clk.BlockUntil(2) below — with the
// budget on the waiting side only, the run arms no timer of its own, so the
// second one this test waits for never exists and nothing can move the clock
// on to the expiry that is the subject.
func TestARunAbandonedByEveryCallerStillExpires(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		registry, clk := newRegistry(t, svchealth.Config{})
		release := make(chan struct{})
		releaseOnce := sync.OnceFunc(func() { close(release) })
		//: whatever happens below, the body is let go before the bubble ends.
		defer releaseOnce()
		runs := make(chan context.Context, 1)
		mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
			Name: "db", Check: func(ctx context.Context) error {
				runs <- ctx
				select {
				//: a body that honours its context, which is the shape this
				//: test is about: the one that CAN be let go, and was not.
				case <-ctx.Done():
					return ctx.Err()
				//: the fallback, so a regression deadlocks the bubble rather
				//: than hanging the whole binary.
				case <-release:
					return nil
				}
			},
		})
		callerCtx, cancel := context.WithCancel(context.Background())
		reports := make(chan corehealth.ReportValue, 1)
		go func() { reports <- registry.Probe(callerCtx, corehealth.ProbeReadiness) }()
		runCtx := <-runs
		//: the probe's timer and the run's.
		clk.BlockUntil(2)
		//: the only caller leaves. Nothing is waiting on the run now.
		cancel()
		<-reports
		synctest.Wait()
		//: departure alone must still not cancel it — that is the neighbouring
		//: test's contract, re-asserted here because this one moves the clock
		//: next and would otherwise not distinguish the two causes.
		if err := runCtx.Err(); err != nil {
			t.Fatalf("the caller's departure cancelled the shared run: %v", err)
		}
		//: only the run's own timer is left; the departed probe stopped its own.
		clk.BlockUntil(1)
		clk.Advance(budget)
		synctest.Wait()
		if err := runCtx.Err(); err == nil {
			t.Fatal("the run was never told its budget was over: <nil>")
		}
	})
}

// TestAProbeJoiningAnExpiredRunReadsATimeout closes the gap the run-owned
// budget opened. Cancelling the run is an ANNOUNCEMENT, and a check that
// HONOURS its context answers it by returning ctx.Err() — an ordinary failure
// wearing no timeout. The probe that was waiting when the budget fired gets
// health.abandoned's verdict either way, but the result the run PUBLISHES is
// what every probe joining afterwards reads, and a plain "context canceled" is
// not what happened: the check was too slow.
//
// The body waits for a release before returning, which is what makes the window
// reachable: the run is expired and still outstanding, so the second probe
// joins it rather than starting its own, and then reads the published result.
//
// Seen failing with the classification removed (perform building the result
// straight from the body's error): "TimedOut = false" and "[0.3.59.1
// CHECK_FAILED] A health check reported a failure" — so a dependency that was
// simply too slow looked like an ordinary failure to every probe after the
// first.
// # Goroutine lifetime
//
// Two probes, each on its own goroutine, plus the body the registry runs. The
// first ends at the clock advance, the second when the release lets the body
// return, and both are received from before the test exits — the body's
// goroutine is the run's, and it ends with it.
func TestAProbeJoiningAnExpiredRunReadsATimeout(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: func(ctx context.Context) error {
			//: only the first entry announces; the run is shared.
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-ctx.Done()
			//: held here so the run stays outstanding while it is joined.
			<-release
			//: the shape under test: a body that honours its cancellation.
			return ctx.Err()
		},
	})
	first := make(chan corehealth.ReportValue, 1)
	go func() { first <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
	<-entered
	//: the probe's timer and the run's own.
	clk.BlockUntil(2)
	clk.Advance(budget)
	//: the waiter's own verdict, which was never in doubt.
	if got := resultFor(t, <-first, "db"); !got.TimedOut {
		t.Error("the waiting probe reported TimedOut = false, want the budget's verdict")
	}
	//: a second probe joins the expired, still-outstanding run.
	second := make(chan corehealth.ReportValue, 1)
	go func() { second <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
	//: it is parked on its own budget over that run; letting the body go now
	//: makes it read the PUBLISHED result rather than its own timeout.
	clk.BlockUntil(1)
	close(release)
	joined := resultFor(t, <-second, "db")
	if !joined.TimedOut {
		t.Errorf("a probe joining the expired run got TimedOut = %v, want true", joined.TimedOut)
	}
	if !errs.HasCode(joined.Err, svchealth.CodeCheckTimeout) {
		t.Errorf("a probe joining the expired run got %v, want CHECK_TIMEOUT", joined.Err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the body ran %d times, want 1 — the second probe must not start its own", got)
	}
}

// TestABudgetExpiryStillHonoursCriticality pins that a non-critical check that
// times out degrades rather than sinks the probe. A timeout is a failure, and
// a failure goes through the same rule every other failure does.
func TestABudgetExpiryStillHonoursCriticality(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	release, cancelled := make(chan struct{}), make(chan struct{})
	defer close(release)
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "recommendations", Check: blocking(release, cancelled), NonCritical: true,
	})
	//: two timers: the probe's, and the run's own budget.
	report := probeUnderClock(t, registry, clk, corehealth.ProbeReadiness, 2)
	if report.Status != corehealth.StatusDegraded {
		t.Errorf("a timed-out non-critical check = %v, want degraded", report.Status)
	}
	if !report.Status.Serving() {
		t.Error("a degraded probe stopped serving; NonCritical then means nothing")
	}
	<-cancelled
}

// TestAWedgedCheckCostsOneGoroutineNotOnePerPoll is the leak guard.
//
// A probe endpoint polled every ten seconds against a dependency call with no
// deadline would otherwise start a goroutine — and hold a connection — every
// ten seconds, for as long as the outage lasts. The leak would be triggered
// precisely by the thing the endpoint was watching for.
//
// The registry instead keeps ONE outstanding run per check: the second probe
// joins it, waits out its own budget, and reports a timeout without ever
// invoking the body again.
func TestAWedgedCheckCostsOneGoroutineNotOnePerPoll(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	release := make(chan struct{})
	defer close(release)
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: wedged(release, &calls),
	})
	//: three consecutive polls, each timing out on its own budget. The FIRST
	//: arms two timers — the probe's and the run's own — while the two that
	//: join the wedged run arm only their own, the run's watcher having
	//: cancelled and exited when its budget fired on poll one.
	//
	//: waiting for that second timer is also what makes the count below
	//: deterministic: the run arms it, so it exists only once the body's
	//: goroutine is running. Waiting for the probe's timer alone used to let
	//: the clock advance before the body had been entered, and the assertion
	//: then read 0 — reliably outside the race lane, never inside it.
	for poll := range 3 {
		waits := 1
		if poll == 0 {
			waits = 2
		}
		report := probeUnderClock(t, registry, clk, corehealth.ProbeReadiness, waits)
		if !resultFor(t, report, "db").TimedOut {
			t.Fatalf("poll %d did not time out", poll)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the wedged check body was entered %d times over 3 polls, want 1 — "+
			"a probe under poll must not be a goroutine factory", got)
	}
}

// TestARunIsNotSticky is the counterpart of the leak guard, and the reason the
// two are not the same mechanism.
//
// A run that COMPLETES is released, so the next probe measures again. Without
// this, "one outstanding run per check" would be indistinguishable from "a
// check is measured once and its answer is kept forever", which is a defect
// wearing the same shape: an endpoint that reports a dependency that recovered
// as still broken, permanently.
func TestARunIsNotSticky(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	//: no MaxAge, so nothing may be replayed: every probe must measure.
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: passing(&calls),
	})
	for range 3 {
		if got := registry.Probe(context.Background(), corehealth.ProbeReadiness); !got.Status.Serving() {
			t.Fatalf("probe = %v, want serving", got.Status)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("the check was measured %d times over 3 probes, want 3 — a run "+
			"that finished must be released", got)
	}
}

// TestConcurrentProbesShareOneMeasurement pins the other half of the same
// mechanism: two probes overlapping do not produce two dials, and the joiner
// gets the REAL answer rather than a timeout it never waited for.
func TestConcurrentProbesShareOneMeasurement(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: func(_ context.Context) error {
			//: only the first entry announces; the test closes it once.
			if calls.Add(1) == 1 {
				close(entered)
			}
			<-release
			return nil
		},
	})
	reports := make(chan corehealth.ReportValue, 2)
	go func() { reports <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
	<-entered
	go func() { reports <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
	//: three timers: one per waiting probe, plus the run's own budget.
	clk.BlockUntil(3)
	close(release)
	for range 2 {
		report := <-reports
		if report.Status != corehealth.StatusHealthy {
			t.Errorf("an overlapping probe = %v, want healthy — a joiner must get "+
				"the real answer, not a verdict it did not wait for", report.Status)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("the body ran %d times for two overlapping probes, want 1", got)
	}
}

// TestAPanicInACheckIsTheCheckFailingNotTheProcessDying pins the recovery. A
// panic escaping a check would take the whole process down over a health
// question — the endpoint becoming the outage it was watching for.
func TestAPanicInACheckIsTheCheckFailingNotTheProcessDying(t *testing.T) {
	t.Parallel()
	registry, _ := newRegistry(t, svchealth.Config{})
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "db", Check: func(_ context.Context) error {
			panic("nil map write at 10.0.3.14:5432")
		},
	})
	report := registry.Probe(context.Background(), corehealth.ProbeReadiness)
	result := resultFor(t, report, "db")
	if result.Status != corehealth.StatusUnhealthy {
		t.Errorf("a panicking check = %v, want unhealthy", result.Status)
	}
	if !errs.HasCode(result.Err, corehealth.CodeCheckPanicked) {
		t.Errorf("the result carries %v, want CHECK_PANICKED", result.Err)
	}
	//: the panic value travels as a FIELD, never as the wrap origin, so it
	//: cannot become the Public a stranger reads on the probe endpoint.
	if public := errs.PublicOf(result.Err); public != corehealth.CheckPanicked.Public() {
		t.Errorf("the public half is %q, want the sentinel's — a panic string "+
			"routinely carries an address", public)
	}
}

// TestOnlySuccessesAreCached pins all three bounds on staleness at once: a
// success replays inside its window, it stops replaying outside it, and a
// FAILURE is never replayed at all.
//
// The asymmetry is the point. A cached success saves the cost of an answer
// already held; a cached failure would only postpone the answer an operator
// actually needs promptly, which is the one saying the outage ended.
func TestOnlySuccessesAreCached(t *testing.T) {
	t.Parallel()
	registry, clk := newRegistry(t, svchealth.Config{})
	var calls atomic.Int64
	failing := false
	window := 10 * time.Second
	mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
		Name: "expensive", MaxAge: window, Check: func(_ context.Context) error {
			calls.Add(1)
			if failing {
				return errDependency
			}
			return nil
		},
	})
	ctx := context.Background()
	first := registry.Probe(ctx, corehealth.ProbeReadiness)
	if resultFor(t, first, "expensive").Cached {
		t.Error("the first answer is marked cached; nothing had been measured yet")
	}
	//: inside the window: replayed, and marked as a dated statement.
	clk.Advance(window / 2)
	replayed := resultFor(t, registry.Probe(ctx, corehealth.ProbeReadiness), "expensive")
	if !replayed.Cached || calls.Load() != 1 {
		t.Errorf("inside the window the check ran %d times and Cached=%v, want 1 and true",
			calls.Load(), replayed.Cached)
	}
	if got := replayed.Age(clk.Now()); got != window/2 {
		t.Errorf("the replay reports age %v, want %v — a cached answer must be "+
			"dated, not presented as current", got, window/2)
	}
	//: past the window: measured again.
	clk.Advance(window)
	failing = true
	if resultFor(t, registry.Probe(ctx, corehealth.ProbeReadiness), "expensive").Cached {
		t.Error("an answer older than MaxAge was replayed")
	}
	//: and that failure is never replayed, however fresh it is.
	before := calls.Load()
	registry.Probe(ctx, corehealth.ProbeReadiness)
	if calls.Load() != before+1 {
		t.Error("a failing check was served from the cache; recovery would then be " +
			"invisible for a whole window")
	}
}

// TestABudgetIsNeverZero pins ADR 0031's clamp. A zero is what an unset field
// looks like, and reading it as "no time at all" would turn a forgotten line
// into a probe where every check fails before it runs.
//
// Each case runs its probe on a background goroutine so the clock can be moved
// while the probe is blocked on its budget. That goroutine ends when the probe
// answers, which the buffered channel guarantees it can do whether or not the
// case is still reading; the check body it started ends at the deferred
// close(release), and <-cancelled proves the abandoned run was told so.
func TestABudgetIsNeverZero(t *testing.T) {
	t.Parallel()
	type tc struct {
		name           string
		defaultTimeout time.Duration
		checkTimeout   time.Duration
		want           time.Duration
	}
	tests := []tc{
		{"nothing set at all", 0, 0, svchealth.DefaultCheckTimeout},
		{"a negative registry default", -time.Hour, 0, svchealth.DefaultCheckTimeout},
		{"the registry default", 5 * time.Second, 0, 5 * time.Second},
		{"a negative check budget falls back", 5 * time.Second, -time.Hour, 5 * time.Second},
		{"the check's own budget wins", 5 * time.Second, 250 * time.Millisecond, 250 * time.Millisecond},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			//: exact, because this test asserts what the SDK's own clamps
			//: resolve to — a harness supplying a default would assert itself.
			registry, clk := newExactRegistry(t, svchealth.Config{DefaultTimeout: c.defaultTimeout})
			release, cancelled := make(chan struct{}), make(chan struct{})
			defer close(release)
			mustAddReadiness(t, registry, corehealth.ReadinessCheckValue{
				Name: "db", Check: blocking(release, cancelled), Timeout: c.checkTimeout,
			})
			reports := make(chan corehealth.ReportValue, 1)
			go func() { reports <- registry.Probe(context.Background(), corehealth.ProbeReadiness) }()
			//: two timers: the probe's, and the run's own budget — both armed
			//: with the same resolved value, which is what this asserts.
			clk.BlockUntil(2)
			//: one nanosecond short of the resolved budget must NOT fire; a
			//: clamp that landed on zero would already have reported by now.
			clk.Advance(c.want - 1)
			select {
			case report := <-reports:
				t.Fatalf("the probe answered %v before its budget elapsed", report.Status)
			default:
			}
			clk.Advance(time.Nanosecond)
			if got := resultFor(t, <-reports, "db"); got.Took != c.want {
				t.Errorf("the resolved budget is %v, want %v", got.Took, c.want)
			}
			<-cancelled
		})
	}
}
