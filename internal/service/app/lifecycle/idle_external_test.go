// Package lifecycle_test — IdleStop, driven by a ManualClock and a probe the
// test answers one call at a time: every check waited for, never slept for.
// That the watcher asks again is the proof it has not stopped; a watcher that
// stopped early leaves the next answer unread, and the test times out.
package lifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	corelc "github.com/kitsunium/sdk/internal/core/app/lifecycle"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svclc "github.com/kitsunium/sdk/internal/service/app/lifecycle"
)

// probe answers IdleStop's checks with what the test sends, one per check.
type probe struct{ answers chan bool }

func newProbe() *probe { return &probe{answers: make(chan bool)} }

func (p *probe) idle() bool { return <-p.answers }

// answer hands the next check its answer.
func (p *probe) answer(idle bool) { p.answers <- idle }

// startIdle builds an IdleStop over a manual clock, a 30 s grace checked
// every 10 s.
func startIdle(t *testing.T, parent context.Context) (context.Context, context.CancelFunc, *probe, *clock.ManualClock) {
	t.Helper()
	clk := clock.NewManualClock(time.Unix(1_000_000, 0))
	p := newProbe()
	ctx, stop, err := svclc.IdleStop(parent, svclc.IdleConfig{Idle: p.idle, Grace: 30 * time.Second, Every: 10 * time.Second, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return ctx, stop, p, clk
}

// tick moves the clock one interval once the watcher waits on its ticker.
func tick(clk *clock.ManualClock) {
	clk.BlockUntil(1)
	clk.Advance(10 * time.Second)
}

func TestIdleStop_stopsOneGraceAfterTheFirstIdleCheck(t *testing.T) {
	ctx, _, p, clk := startIdle(t, context.Background())
	p.answer(true) // at start: the count begins
	for range 3 {  // asked at 10, 20 and 30 s: not stopped before
		tick(clk)
		p.answer(true)
	}
	<-ctx.Done()
	if cause := context.Cause(ctx); !kerrs.HasCode(cause, corelc.CodeIdleReached) || !errors.Is(cause, corelc.IdleReached) {
		t.Fatalf("cause = %v, want IdleReached", cause)
	}
}

func TestIdleStop_aBusyAnswerStartsTheCountAgain(t *testing.T) {
	ctx, _, p, clk := startIdle(t, context.Background())
	p.answer(true)
	tick(clk)
	p.answer(true)
	tick(clk)
	p.answer(false) // busy at 20 s
	for range 4 {   // idle again from 30 s, still asked at 60 s
		tick(clk)
		p.answer(true)
	}
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), corelc.IdleReached) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}
}

func TestIdleStop_endsWithItsParentAndItsCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, _, p, _ := startIdle(t, parent)
	p.answer(false)
	cancelParent()
	<-ctx.Done()
	if context.Cause(ctx) != context.Canceled {
		t.Fatalf("cause = %v, want the parent's", context.Cause(ctx))
	}

	ctx2, stop, p2, _ := startIdle(t, context.Background())
	p2.answer(false)
	stop()
	<-ctx2.Done()
	if context.Cause(ctx2) != context.Canceled {
		t.Fatalf("stop did not end the context: %v", context.Cause(ctx2))
	}
}

func TestIdleStop_refusesWhatCouldNeverDecide(t *testing.T) {
	for name, cfg := range map[string]svclc.IdleConfig{
		"no probe":       {Grace: time.Second},
		"no grace":       {Idle: func() bool { return true }},
		"negative grace": {Idle: func() bool { return true }, Grace: -time.Second},
	} {
		if _, _, err := svclc.IdleStop(context.Background(), cfg); !kerrs.HasCode(err, corelc.CodeIdleMisconfigured) {
			t.Errorf("%s: %v, want IdleMisconfigured", name, err)
		}
	}
}

func TestIdleStop_defaultsEveryToATenthOfTheGrace(t *testing.T) {
	clk := clock.NewManualClock(time.Unix(1_000_000, 0))
	p := newProbe()
	ctx, stop, err := svclc.IdleStop(context.Background(), svclc.IdleConfig{Idle: p.idle, Grace: 10 * time.Second, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p.answer(true)
	for range 10 {
		clk.BlockUntil(1)
		clk.Advance(time.Second)
		p.answer(true)
	}
	<-ctx.Done() // ten one-second checks over a ten-second grace
}
