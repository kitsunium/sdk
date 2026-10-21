// Package lifecycle_test — the supervisor's restart intensity (#482): more
// than Restarts early ends within Within, and the supervision gives up,
// driven on a ManualClock.
package lifecycle_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/app/lifecycle"
)

// flatSecond is a restart backoff that always waits one second.
var flatSecond = kbackoff.Value{BaseDelay: time.Second, MaxDelay: time.Second}

// endingSupervisor builds a supervisor over s whose observer also signals
// the end of every supervision on the returned channel.
func endingSupervisor(t *testing.T, s *script, cfg svclc.SupervisorConfig) (*svclc.Supervisor, *clock.ManualClock, *eventLog, <-chan struct{}) {
	t.Helper()
	clk := clock.NewManualClock(origin)
	log := &eventLog{}
	ended := make(chan struct{}, 4)
	cfg.Clock = clk
	cfg.Observe = func(event svclc.SupervisionEventValue) {
		log.observe(event)
		if event.Phase == svclc.SupervisionStopped {
			ended <- struct{}{}
		}
	}
	sup, err := svclc.NewSupervisor("flappy", s.run, cfg)
	if err != nil {
		t.Fatalf("NewSupervisor() = %v", err)
	}
	return sup, clk, log, ended
}

// fieldOf returns the string value of err's field key, or "".
func fieldOf(err error, key string) string {
	for _, field := range kerrs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// TestSupervisorGivesUpOnTheEarlyEndPastItsIntensity pins Erlang's rule:
// with Restarts 2 within a minute, the first two early ends are restarted,
// the third is not — the supervision ends, its observer hears GaveUp then
// Stopped, and Check reports SupervisorGaveUp with the last run's error. A
// later Start clears it.
func TestSupervisorGivesUpOnTheEarlyEndPastItsIntensity(t *testing.T) {
	s := newScript("error", "panic", "error")
	sup, clk, log, ended := endingSupervisor(t, s, svclc.SupervisorConfig{
		Backoff: flatSecond, GiveUpAfter: svclc.GiveUpValue{Restarts: 2, Within: time.Minute},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	restartAfter(t, clk, s, 2, time.Second)
	restartAfter(t, clk, s, 3, time.Second)
	<-ended
	want := []svclc.SupervisionPhase{
		svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionRestarting,
		svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionRestarting,
		svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionGaveUp, svclc.SupervisionStopped,
	}
	if got := log.phases(); !slices.Equal(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	gaveUp := log.of(svclc.SupervisionGaveUp)[0]
	if !kerrs.HasCode(gaveUp.Err, corelc.CodeSupervisorGaveUp) || gaveUp.Run != 3 || gaveUp.Failures != 3 {
		t.Fatalf("GaveUp event = %+v, want SupervisorGaveUp on run 3 after 3 failures", gaveUp)
	}
	if !gaveUp.At.Equal(origin.Add(2 * time.Second)) {
		t.Fatalf("GaveUp at %v, want %v", gaveUp.At, origin.Add(2*time.Second))
	}
	err := sup.Check(context.Background())
	if !kerrs.HasCode(err, corelc.CodeSupervisorGaveUp) {
		t.Fatalf("Check() = %v, want SupervisorGaveUp", err)
	}
	if got := fieldOf(err, "last"); !strings.Contains(got, errFlap.Error()) {
		t.Fatalf("Check() last = %q, want the last run's error", got)
	}
	if got := fieldOf(err, "supervisor"); got != "flappy" {
		t.Fatalf("Check() supervisor = %q, want flappy", got)
	}
	if clk.Pending() != 0 {
		t.Fatalf("%d timers pending after giving up, want none", clk.Pending())
	}
	if err := sup.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() after giving up = %v, want nil", err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() after giving up = %v", err)
	}
	if err := sup.Check(context.Background()); err != nil {
		t.Fatalf("Check() after a new Start = %v, want nil", err)
	}
	<-s.started
	stop(t, sup)
	<-ended
}

// TestSupervisorKeepsRestartingEarlyEndsSpreadPastTheSpan pins the span: early
// ends a minute apart never put two within ten seconds, so a Restarts of 1
// never gives up, and Check stays nil throughout.
func TestSupervisorKeepsRestartingEarlyEndsSpreadPastTheSpan(t *testing.T) {
	s := newScript("error", "error", "error", "error")
	minute := kbackoff.Value{BaseDelay: time.Minute, MaxDelay: time.Minute}
	sup, clk, log, ended := endingSupervisor(t, s, svclc.SupervisorConfig{
		Backoff: minute, GiveUpAfter: svclc.GiveUpValue{Restarts: 1, Within: 10 * time.Second},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	for next := 2; next <= 5; next++ {
		restartAfter(t, clk, s, next, time.Minute)
		if err := sup.Check(context.Background()); err != nil {
			t.Fatalf("Check() before run %d = %v, want nil", next, err)
		}
	}
	stop(t, sup)
	<-ended
	if gaveUp := log.of(svclc.SupervisionGaveUp); len(gaveUp) != 0 {
		t.Fatalf("GaveUp events = %v, want none", gaveUp)
	}
}

// TestSupervisorWithZeroRestartsGivesUpAtTheFirstEarlyEnd pins Erlang's
// intensity 0: a positive Within with no restart allowed ends the supervision
// on the first early end, with no Restarting event and no timer armed.
func TestSupervisorWithZeroRestartsGivesUpAtTheFirstEarlyEnd(t *testing.T) {
	s := newScript("return")
	sup, clk, log, ended := endingSupervisor(t, s, svclc.SupervisorConfig{
		GiveUpAfter: svclc.GiveUpValue{Within: time.Second},
	})
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	<-ended
	want := []svclc.SupervisionPhase{
		svclc.SupervisionRunStarted, svclc.SupervisionRunEnded, svclc.SupervisionGaveUp, svclc.SupervisionStopped,
	}
	if got := log.phases(); !slices.Equal(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
	err := sup.Check(context.Background())
	if !kerrs.HasCode(err, corelc.CodeSupervisorGaveUp) || fieldOf(err, "last") != "" {
		t.Fatalf("Check() = %v, want SupervisorGaveUp with no last error (the run returned nil)", err)
	}
	if clk.Pending() != 0 {
		t.Fatalf("%d timers pending, want none", clk.Pending())
	}
}

// TestSupervisorCheckIsNilBeforeAndWhileItRuns pins Check's other answers: a
// supervisor never started, and one running, report nil.
func TestSupervisorCheckIsNilBeforeAndWhileItRuns(t *testing.T) {
	s := newScript()
	sup, _, _, ended := endingSupervisor(t, s, svclc.SupervisorConfig{
		GiveUpAfter: svclc.GiveUpValue{Restarts: 1, Within: time.Second},
	})
	if err := sup.Check(context.Background()); err != nil {
		t.Fatalf("Check() before Start = %v, want nil", err)
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sup.Check(done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Check(done ctx) = %v, want the context's own error", err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	<-s.started
	if err := sup.Check(context.Background()); err != nil {
		t.Fatalf("Check() while running = %v, want nil", err)
	}
	stop(t, sup)
	<-ended
	if err := sup.Check(context.Background()); err != nil {
		t.Fatalf("Check() after Stop = %v, want nil", err)
	}
}

// TestNewSupervisorRefusesAGiveUpThatIsNoBound pins the refusals: a negative
// field, and restarts counted over no span, are SupervisorMisconfigured
// naming the field; the zero value and Restarts 0 with a span are accepted.
func TestNewSupervisorRefusesAGiveUpThatIsNoBound(t *testing.T) {
	run := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	refused := []struct {
		giveUp svclc.GiveUpValue
		field  string
	}{
		{svclc.GiveUpValue{Restarts: -1, Within: time.Second}, "GiveUpAfter.Restarts"},
		{svclc.GiveUpValue{Restarts: 1, Within: -time.Second}, "GiveUpAfter.Within"},
		{svclc.GiveUpValue{Within: -time.Second}, "GiveUpAfter.Within"},
		{svclc.GiveUpValue{Restarts: 3}, "GiveUpAfter.Within"},
	}
	for _, c := range refused {
		sup, err := svclc.NewSupervisor("bounded", run, svclc.SupervisorConfig{GiveUpAfter: c.giveUp})
		if sup != nil || !kerrs.HasCode(err, corelc.CodeSupervisorMisconfigured) {
			t.Fatalf("NewSupervisor(%+v) = %v, %v, want SupervisorMisconfigured", c.giveUp, sup, err)
		}
		if got := fieldOf(err, "argument"); got != c.field {
			t.Fatalf("NewSupervisor(%+v) argument = %q, want %q", c.giveUp, got, c.field)
		}
	}
	for _, giveUp := range []svclc.GiveUpValue{{}, {Within: time.Second}} {
		if _, err := svclc.NewSupervisor("bounded", run, svclc.SupervisorConfig{GiveUpAfter: giveUp}); err != nil {
			t.Fatalf("NewSupervisor(%+v) = %v, want accepted", giveUp, err)
		}
	}
}

// TestSupervisionGaveUpRendersItsName pins the fifth phase's spelling.
func TestSupervisionGaveUpRendersItsName(t *testing.T) {
	if got := svclc.SupervisionGaveUp.String(); got != "gave up" {
		t.Fatalf("SupervisionGaveUp.String() = %q, want %q", got, "gave up")
	}
}
