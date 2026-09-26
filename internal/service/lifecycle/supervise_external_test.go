// Package lifecycle_test — the supervisor, driven by a ManualClock: every
// restart waited for exactly, never slept for.
package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/lifecycle"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// errFlap is what a scripted run returns when it fails.
var errFlap = errors.New("flap")

// script is a supervised function whose every run does what the next step
// says — fail, panic, return early, or wait for its context — and reports
// that it started.
type script struct {
	// started receives the number of each run as it begins.
	started chan int
	// steps are the runs' actions, in order; past the end, a run waits.
	steps []string
	mu    sync.Mutex
	runs  int
}

// newScript returns a script over steps.
func newScript(steps ...string) *script {
	return &script{started: make(chan int, 16), steps: steps}
}

// run is the supervised function.
func (s *script) run(ctx context.Context) error {
	s.mu.Lock()
	s.runs++
	run := s.runs
	step := "wait"
	if run <= len(s.steps) {
		step = s.steps[run-1]
	}
	s.mu.Unlock()
	s.started <- run
	switch step {
	case "error":
		return errFlap
	case "panic":
		panic("canary: do-not-leak-4f5a")
	case "return":
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

// eventLog collects a supervisor's events.
type eventLog struct {
	mu     sync.Mutex
	events []svclc.SupervisionEventValue
}

// observe is the supervisor's Observe.
func (l *eventLog) observe(event svclc.SupervisionEventValue) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

// phases returns the events' phases, in order.
func (l *eventLog) phases() []svclc.SupervisionPhase {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]svclc.SupervisionPhase, len(l.events))
	for i, event := range l.events {
		out[i] = event.Phase
	}
	return out
}

// of returns the events of one phase, in order.
func (l *eventLog) of(phase svclc.SupervisionPhase) []svclc.SupervisionEventValue {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []svclc.SupervisionEventValue
	for _, event := range l.events {
		if event.Phase == phase {
			out = append(out, event)
		}
	}
	return out
}

// drivenClock is what the helpers below drive a ManualClock through.
type drivenClock interface {
	Advance(d time.Duration)
	BlockUntil(n int)
	Pending() int
}

// newSupervisor builds a supervisor over run with a manual clock and a log.
func newSupervisor(t *testing.T, run func(context.Context) error, cfg svclc.SupervisorConfig) (*svclc.Supervisor, *clock.ManualClock, *eventLog) {
	t.Helper()
	clk := clock.NewManualClock(origin)
	log := &eventLog{}
	cfg.Clock, cfg.Observe = clk, log.observe
	sup, err := svclc.NewSupervisor("flappy", run, cfg)
	if err != nil {
		t.Fatalf("NewSupervisor() = %v", err)
	}
	return sup, clk, log
}

// restartAfter pins one backoff: the supervisor is waiting, it does not run
// again one nanosecond early, and it runs again at exactly delay.
func restartAfter(t *testing.T, clk drivenClock, s *script, next int, delay time.Duration) {
	t.Helper()
	clk.BlockUntil(1)
	clk.Advance(delay - time.Nanosecond)
	if pending := clk.Pending(); pending != 1 {
		t.Fatalf("before %s the backoff timer is gone (%d pending): the run restarted early", delay, pending)
	}
	clk.Advance(time.Nanosecond)
	if got := <-s.started; got != next {
		t.Fatalf("run %d started, want %d", got, next)
	}
}

// stop stops sup within a generous budget the test controls.
func stop(t *testing.T, sup *svclc.Supervisor) {
	t.Helper()
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
}

// TestTheSupervisorRestartsAfterEveryEarlyEnd is kit's hand-written loop
// supervision, pinned on the SDK's: an error, a panic and an early nil return
// each restart the run, after a backoff doubling from a second; the panic is
// recovered into RunPanicked without its value in the Public text; Stop
// cancels the running function's context and waits for it.
func TestTheSupervisorRestartsAfterEveryEarlyEnd(t *testing.T) {
	t.Parallel()
	s := newScript("error", "panic", "return")
	sup, clk, log := newSupervisor(t, s.run, svclc.SupervisorConfig{})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if got := <-s.started; got != 1 {
		t.Fatalf("run %d started first", got)
	}
	restartAfter(t, clk, s, 2, time.Second)   // after the error
	restartAfter(t, clk, s, 3, 2*time.Second) // after the panic
	restartAfter(t, clk, s, 4, 4*time.Second) // after the early return
	stop(t, sup)

	ended := log.of(svclc.SupervisionRunEnded)
	if len(ended) != 4 {
		t.Fatalf("%d runs ended, want 4: %v", len(ended), log.phases())
	}
	if !errors.Is(ended[0].Err, errFlap) {
		t.Errorf("run 1 ended with %v, want the function's own error", ended[0].Err)
	}
	if !kerrs.HasCode(ended[1].Err, svclc.CodeRunPanicked) {
		t.Errorf("run 2 ended with %v, want RunPanicked", ended[1].Err)
	}
	if public := kerrs.PublicOf(ended[1].Err); public != "The supervised function panicked and was recovered" {
		t.Errorf("the panic's Public text is %q", public)
	}
	if ended[2].Err != nil || ended[2].Stopping {
		t.Errorf("run 3 (an early nil return) ended with %+v", ended[2])
	}
	if ended[3].Err != nil || !ended[3].Stopping {
		t.Errorf("run 4 ended with %+v, want a clean stop", ended[3])
	}
	restarts := log.of(svclc.SupervisionRestarting)
	delays := make([]time.Duration, len(restarts))
	for i, r := range restarts {
		delays[i] = r.Delay
	}
	if !slices.Equal(delays, []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}) {
		t.Errorf("the restarts waited %v", delays)
	}
	if last := log.phases(); last[len(last)-1] != svclc.SupervisionStopped {
		t.Errorf("the supervision's last event is %v", last[len(last)-1])
	}
	for _, event := range log.of(svclc.SupervisionRunStarted) {
		if event.Name != "flappy" {
			t.Fatalf("an event carries the name %q", event.Name)
		}
	}
}

// TestAHealthyRunStartsTheCountAgain pins the reset: a run that lasted
// HealthyAfter was working, so its failure waits the first backoff again
// rather than the next one.
func TestAHealthyRunStartsTheCountAgain(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(origin)
	runs := make(chan int, 8)
	var n int
	var mu sync.Mutex
	run := func(ctx context.Context) error {
		mu.Lock()
		n++
		this := n
		mu.Unlock()
		runs <- this
		//: the second run works for a while, then fails.
		if this == 2 {
			select {
			case <-clk.After(time.Minute):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if this <= 2 {
			return errFlap
		}
		<-ctx.Done()
		return ctx.Err()
	}
	log := &eventLog{}
	sup, err := svclc.NewSupervisor("healthy", run, svclc.SupervisorConfig{Clock: clk, Observe: log.observe, HealthyAfter: time.Minute})
	if err != nil {
		t.Fatalf("NewSupervisor() = %v", err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-runs
	clk.BlockUntil(1)
	clk.Advance(time.Second)
	<-runs
	clk.BlockUntil(1) // the second run's minute of work
	clk.Advance(time.Minute)
	clk.BlockUntil(1) // the restart's backoff
	clk.Advance(time.Second)
	<-runs
	stop(t, sup)
	restarts := log.of(svclc.SupervisionRestarting)
	if len(restarts) != 2 || restarts[0].Failures != 1 || restarts[1].Failures != 1 || restarts[1].Delay != time.Second {
		t.Fatalf("restarts %+v, want both a first failure waiting a second", restarts)
	}
}

// TestStopDuringTheBackoff pins that a supervision stopped while it waits to
// restart ends there: no further run.
func TestStopDuringTheBackoff(t *testing.T) {
	t.Parallel()
	s := newScript("error")
	sup, clk, log := newSupervisor(t, s.run, svclc.SupervisorConfig{})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	clk.BlockUntil(1)
	stop(t, sup)
	if got := len(log.of(svclc.SupervisionRunStarted)); got != 1 {
		t.Fatalf("%d runs started, want 1", got)
	}
	if phases := log.phases(); phases[len(phases)-1] != svclc.SupervisionStopped {
		t.Fatalf("phases %v", phases)
	}
}

// TestStopThatOverrunsAbandonsNothing pins the budget: a function that ignores
// its context makes Stop return StopTimeout when the caller stops waiting —
// nothing is killed — and a later Stop, once it returns, succeeds.
func TestStopThatOverrunsAbandonsNothing(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	entered := make(chan struct{})
	stubborn := func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	sup, _, _ := newSupervisor(t, stubborn, svclc.SupervisorConfig{})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-entered
	budget, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.Stop(budget); !kerrs.HasCode(err, svclc.CodeStopTimeout) {
		t.Fatalf("Stop() past its budget = %v, want StopTimeout", err)
	}
	if err := sup.Start(context.Background()); !kerrs.HasCode(err, svclc.CodeSupervisorRunning) {
		t.Fatalf("Start() while the abandoned run is still going = %v, want SupervisorRunning", err)
	}
	close(release)
	stop(t, sup)
}

// TestStartsStopsAndStartsAgain pins the supervisor's own lifecycle: a second
// Start is refused while running, Start after Stop supervises again, a Start
// whose context is already done is refused with that context's error, and the
// Start context's VALUES reach every run while its cancellation ends nothing.
func TestStartsStopsAndStartsAgain(t *testing.T) {
	t.Parallel()
	type key struct{}
	values := make(chan any, 4)
	run := func(ctx context.Context) error {
		values <- ctx.Value(key{})
		<-ctx.Done()
		return ctx.Err()
	}
	sup, _, _ := newSupervisor(t, run, svclc.SupervisorConfig{})
	aborted, abort := context.WithCancel(context.Background())
	abort()
	if err := sup.Start(aborted); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() with a done context = %v", err)
	}
	startCtx, endStart := context.WithCancel(context.WithValue(context.Background(), key{}, "carried"))
	if err := sup.Start(startCtx); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if got := <-values; got != "carried" {
		t.Fatalf("the run saw %v", got)
	}
	endStart() // the START's context ends; the supervision must not
	if err := sup.Start(context.Background()); !kerrs.HasCode(err, svclc.CodeSupervisorRunning) {
		t.Fatalf("a second Start = %v, want SupervisorRunning", err)
	}
	stop(t, sup)
	stop(t, sup) // stopping twice is stopping once
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() after Stop = %v", err)
	}
	<-values
	stop(t, sup)
}

// TestTheSupervisorIsALifecycleComponent pins Component: registered in a
// Lifecycle, the supervision starts with it and is joined by its Stop.
func TestTheSupervisorIsALifecycleComponent(t *testing.T) {
	t.Parallel()
	s := newScript()
	sup, clk, log := newSupervisor(t, s.run, svclc.SupervisorConfig{})
	app := svclc.New(svclc.Config{Clock: clk})
	component := sup.Component()
	if component.Name != "flappy" {
		t.Fatalf("the component is named %q", component.Name)
	}
	if err := app.Add(component); err != nil {
		t.Fatalf("Add() = %v", err)
	}
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	if err := app.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() = %v", err)
	}
	if phases := log.phases(); !slices.Equal(phases, []svclc.SupervisionPhase{
		svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionStopped,
	}) {
		t.Fatalf("phases %v", phases)
	}
}

// TestACustomBackoffIsTheCurve pins that the configured curve is the one the
// restarts wait, ceiling included.
func TestACustomBackoffIsTheCurve(t *testing.T) {
	t.Parallel()
	s := newScript("error", "error", "error")
	sup, clk, log := newSupervisor(t, s.run, svclc.SupervisorConfig{
		Backoff: svcres.BackoffValue{BaseDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	restartAfter(t, clk, s, 2, 10*time.Millisecond)
	restartAfter(t, clk, s, 3, 20*time.Millisecond)
	restartAfter(t, clk, s, 4, 20*time.Millisecond)
	stop(t, sup)
	if got := len(log.of(svclc.SupervisionRestarting)); got != 3 {
		t.Fatalf("%d restarts", got)
	}
}

// TestACurveWithoutABaseNeverRestartsAtOnce pins the clamp on a partial
// curve: a Backoff with a ceiling and a factor but no BaseDelay would restart
// at once — a hot loop — so it starts from DefaultRestartBase, and the
// caller's factor still shapes it.
func TestACurveWithoutABaseNeverRestartsAtOnce(t *testing.T) {
	t.Parallel()
	s := newScript("error", "error")
	sup, clk, _ := newSupervisor(t, s.run, svclc.SupervisorConfig{
		Backoff: svcres.BackoffValue{MaxDelay: time.Hour, Multiplier: 3},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	restartAfter(t, clk, s, 2, svclc.DefaultRestartBase)
	restartAfter(t, clk, s, 3, 3*svclc.DefaultRestartBase)
	stop(t, sup)
}

// TestNewSupervisorRefuses pins the two refusals: no name, no function.
func TestNewSupervisorRefuses(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "", run: func(context.Context) error { return nil }},
		{name: "nothing", run: nil},
	} {
		if sup, err := svclc.NewSupervisor(c.name, c.run, svclc.SupervisorConfig{}); !kerrs.HasCode(err, svclc.CodeSupervisorMisconfigured) || sup != nil {
			t.Errorf("NewSupervisor(%q) = %v, %v", c.name, sup, err)
		}
	}
	if phase := svclc.SupervisionPhase(9); phase.String() != "unknown" {
		t.Errorf("an unknown phase renders %q", phase)
	}
	for _, phase := range []svclc.SupervisionPhase{svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionRestarting, svclc.SupervisionStopped} {
		if phase.String() == "unknown" {
			t.Errorf("phase %d renders unknown", phase)
		}
	}
}
