package app

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What the framework restarts after a failure — a consumer, a hand-written
// loop, a declared loop — it gives up once it failed more often than its
// intensity allows (GiveUpAfter): the component stops, its liveness check
// "supervised:<node>" fails with the reason, and the problem says so. The
// tests run on a manual clock and move it a second at a time, yielding in
// between, never sleeping.

// errBoom is a product's failure.
var errBoom = errs.New(CodeLoopStart, "BOOM", "boom", "the test's failure")

// startSupervised runs svc in memory on a manual clock and returns the app
// and its clock.
func startSupervised(t *testing.T, svc *Service) (*App, *clock.ManualClock) {
	t.Helper()
	clk := manualAt()
	a := NewApp(svc.name, svc).With(InMemory(), Clock(clk), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, a.Start(t.Context()))
	stopAtCleanup(t, a)
	return a, clk
}

// liveness is the result of the liveness check named check, and the
// probe's status.
func liveness(t *testing.T, a *App, check string) (health.Result, health.Status) {
	t.Helper()
	report := a.health.Probe(t.Context(), health.ProbeLiveness)
	for _, r := range report.Results {
		if r.Name == check {
			return r, report.Status
		}
	}
	t.Fatalf("no liveness check %s among %+v", check, report.Results)
	return health.Result{}, report.Status
}

// loopOf is the loop called name as the Studio shows it.
func loopOf(a *App, name string) model.Loop {
	l, _ := consumerLoop(a, name)
	return l
}

// advanceUntil moves clk a second at a time, yielding between moves, until
// cond holds; it fails the test once the clock moved more than limit.
func advanceUntil(t *testing.T, clk *clock.ManualClock, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	began := clk.Now()
	for !cond() {
		if clk.Since(began) > limit {
			t.Fatalf("the clock moved %v and still not %s", clk.Since(began), what)
		}
		clk.Advance(time.Second)
		for range 64 {
			if cond() {
				return
			}
			runtime.Gosched()
		}
	}
}

// gaveUpSaid reports whether the app's problems say node was given up.
func gaveUpSaid(a *App, node string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range a.problems {
		if p.Node == node && strings.Contains(p.Message, "no longer live") {
			return true
		}
	}
	return false
}

// failingRoutine is a hand-written loop that fails each time the test says
// so, and otherwise runs until its context ends.
type failingRoutine struct {
	fail chan struct{}
	r    *Routine
}

// newFailingRoutine declares it in svc, with opts.
func newFailingRoutine(svc *Service, opts ...RoutineConfigurer) *failingRoutine {
	f := &failingRoutine{fail: make(chan struct{})}
	f.r = svc.Go("sweep", func(ctx context.Context) error {
		select {
		case <-f.fail:
			return errBoom
		case <-ctx.Done():
			return nil
		}
	}, opts...)
	return f
}

// A hand-written loop that fails more often than its intensity allows is
// given up: restarted once, then, at its second failure within the span,
// stopped; its liveness check answers the SDK's SupervisorGaveUp, the probe
// is unhealthy, its loop is stopped and says why, and the problem names it.
func TestARoutineThatFailsTooOftenIsGivenUp(t *testing.T) {
	svc := NewService("sweeper", "A routine that fails when told.")
	f := newFailingRoutine(svc, GiveUpAfter(1, 24*time.Hour))
	a, clk := startSupervised(t, svc)
	check := supervisedCheck + f.r.id
	if r, status := liveness(t, a, check); r.Err != nil || status != health.StatusHealthy {
		t.Fatalf("a routine that runs: %+v, %v", r, status)
	}
	f.fail <- struct{}{}
	advanceUntil(t, clk, time.Hour, "restarted", func() bool { return loopOf(a, f.r.id).Restarts == 1 })
	f.fail <- struct{}{}
	until(t, "the routine to be given up", func() bool {
		r, _ := liveness(t, a, check)
		return r.Err != nil
	})
	r, status := liveness(t, a, check)
	if !errors.Is(r.Err, lifecycle.SupervisorGaveUp) || status != health.StatusUnhealthy {
		t.Errorf("a routine given up: %v, %v; want SupervisorGaveUp, unhealthy", r.Err, status)
	}
	until(t, "the loop to be stopped", func() bool { return loopOf(a, f.r.id).State == model.LoopStopped })
	if l := loopOf(a, f.r.id); l.Restarts != 1 || l.Errors != 2 || l.LastError == "" {
		t.Errorf("the routine's loop: %+v; want 1 restart, 2 errors, the reason", l)
	}
	until(t, "a problem to say it was given up", func() bool { return gaveUpSaid(a, f.r.id) })
}

// A hand-written loop whose failures are further apart than the span is
// restarted every time: the span counts the failures within it, not all of
// them.
func TestARoutineWhoseFailuresAreFarApartIsRestarted(t *testing.T) {
	svc := NewService("sweeper", "A routine that fails now and then.")
	f := newFailingRoutine(svc, GiveUpAfter(1, time.Minute))
	a, clk := startSupervised(t, svc)
	check := supervisedCheck + f.r.id
	f.fail <- struct{}{}
	advanceUntil(t, clk, 30*time.Second, "restarted", func() bool { return loopOf(a, f.r.id).Restarts == 1 })
	clk.Advance(2 * time.Minute)
	f.fail <- struct{}{}
	advanceUntil(t, clk, 30*time.Second, "restarted again", func() bool { return loopOf(a, f.r.id).Restarts == 2 })
	if r, status := liveness(t, a, check); r.Err != nil || status != health.StatusHealthy {
		t.Errorf("a routine that fails once a span: %v, %v; want it live", r.Err, status)
	}
}

// failingLoop is a declared loop whose runs fail while fail is set.
type failingLoop struct {
	fail atomic.Bool
	l    *Loop
}

// newFailingLoop declares it in svc, failing from its first run, with opts.
func newFailingLoop(svc *Service, opts ...LoopConfigurer) *failingLoop {
	f := &failingLoop{}
	f.fail.Store(true)
	f.l = svc.Loop("tick", func(context.Context, WakeEvent) error {
		if f.fail.Load() {
			return errBoom
		}
		return nil
	}, append([]LoopConfigurer{WakeEvery(time.Hour)}, opts...)...)
	return f
}

// A declared loop the framework runs never returns: its failed runs count.
// More than its intensity within the span, and it stops: its liveness check
// answers CodeGaveUp, its loop is stopped, a nudge runs it no more.
func TestADeclaredLoopThatFailsTooOftenIsGivenUp(t *testing.T) {
	svc := NewService("ticker", "A loop whose runs fail.")
	f := newFailingLoop(svc, GiveUpAfter(1, time.Hour))
	a, _ := startSupervised(t, svc)
	check := supervisedCheck + f.l.id
	until(t, "the first run to fail", func() bool { return loopOf(a, f.l.id).Errors == 1 })
	if r, _ := liveness(t, a, check); r.Err != nil {
		t.Fatalf("a loop that failed once: %v", r.Err)
	}
	f.l.Nudge() // a nudge cuts the backoff short: the second failure comes now
	until(t, "the loop to be given up", func() bool {
		r, _ := liveness(t, a, check)
		return r.Err != nil
	})
	r, status := liveness(t, a, check)
	if !errs.HasCode(r.Err, CodeGaveUp) || status != health.StatusUnhealthy {
		t.Errorf("a loop given up: %v, %v; want CodeGaveUp, unhealthy", r.Err, status)
	}
	f.l.Nudge()
	for range 64 {
		runtime.Gosched()
	}
	if l := loopOf(a, f.l.id); l.State != model.LoopStopped || l.Runs != 2 || l.LastError == "" {
		t.Errorf("the loop given up: %+v; want stopped after 2 runs, saying why", l)
	}
	until(t, "a problem to say it was given up", func() bool { return gaveUpSaid(a, f.l.id) })
}

// GiveUpAfter(0, 0) never gives up: a declared loop fails run after run and
// stays live, as a loop always did.
func TestALoopThatNeverGivesUpStaysLive(t *testing.T) {
	svc := NewService("ticker", "A loop whose runs fail, never given up.")
	f := newFailingLoop(svc, GiveUpAfter(0, 0))
	a, _ := startSupervised(t, svc)
	for n := int64(1); n <= 4; n++ {
		until(t, "a run to fail", func() bool { return loopOf(a, f.l.id).Errors == n })
		f.l.Nudge()
	}
	if r, status := liveness(t, a, supervisedCheck+f.l.id); r.Err != nil || status != health.StatusHealthy {
		t.Errorf("a loop that never gives up: %v, %v", r.Err, status)
	}
	if l := loopOf(a, f.l.id); l.State == model.LoopStopped {
		t.Errorf("the loop stopped: %+v", l)
	}
}

// A consumer whose intensity allows no restart is given up at its queue's
// first failure: its liveness check answers the SDK's SupervisorGaveUp, its
// loop stops without a restart.
func TestAConsumerThatAllowsNoRestartIsGivenUpAtOnce(t *testing.T) {
	svc := NewService("flakysub", "A subscription given up at its first failure.")
	letters := svc.Topic[letter2]("letters")
	sub := svc.Subscribe("read", letters, func(context.Context, letter2) error { return nil }, GiveUpAfter(0, time.Hour))
	a, _, _ := startFlaky(t, svc)
	check := supervisedCheck + sub.id
	until(t, "the consumer to be given up", func() bool {
		r, _ := liveness(t, a, check)
		return r.Err != nil
	})
	if r, status := liveness(t, a, check); !errors.Is(r.Err, lifecycle.SupervisorGaveUp) || status != health.StatusUnhealthy {
		t.Errorf("a consumer given up: %v, %v", r.Err, status)
	}
	until(t, "the consumer's loop to stop", func() bool { return loopOf(a, sub.id+" consumer").State == model.LoopStopped })
	if l := loopOf(a, sub.id+" consumer"); l.Restarts != 0 || l.LastError == "" {
		t.Errorf("the consumer's loop: %+v", l)
	}
	until(t, "a problem to say it was given up", func() bool { return gaveUpSaid(a, sub.id) })
}

// endsWithin keeps the failures one span holds, oldest first, in place.
func TestEndsWithinKeepsOneSpan(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	var ends []time.Time
	for _, s := range []int{0, 10, 50, 61, 125} {
		ends = endsWithin(ends, at.Add(time.Duration(s)*time.Second), time.Minute)
	}
	if len(ends) != 1 || !ends[0].Equal(at.Add(125*time.Second)) {
		t.Errorf("after 125s: %v", ends)
	}
	if got := endsWithin([]time.Time{at}, at, 0); len(got) != 0 {
		t.Errorf("no span keeps %v", got)
	}
}
