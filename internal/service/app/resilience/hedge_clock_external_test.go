// Package resilience_test — the hedging policy measuring its delay on an
// injected clock.
package resilience_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// TestHedgeWaitsOnItsClock pins that the duplicate is issued when the clock the
// hedge was given passes Delay, and not a nanosecond before: a minute of
// manual time, no wall-clock time at all.
//
// Before HedgeConfig.Clock the delay ran on a real ticker, so a test of a
// hedged component either waited out its Delay or configured one too small to
// mean anything.
//
// GOROUTINE LIFECYCLE: one goroutine runs the hedge; the first copy blocks
// until the call's context is cancelled, which Run does on its way out once
// the duplicate has won, and the test reads the result before returning.
func TestHedgeWaitsOnItsClock(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		delay time.Duration
	}
	tests := []tc{
		{"a one-minute delay", time.Minute},
		{"a one-hour delay costs no more", time.Hour},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
		h := svcres.NewHedge(svcres.HedgeConfig{
			Idempotent: true, Delay: c.delay, MaxHedges: 1, MaxInFlight: 1, Clock: manual,
		})
		var calls atomic.Int32
		firstStarted := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- h.Run(t.Context(), func(ctx context.Context) error {
				//: the first copy is the slow one: it waits to be cancelled.
				if calls.Add(1) == 1 {
					close(firstStarted)
					<-ctx.Done()
					return ctx.Err()
				}
				//: the duplicate answers at once.
				return nil
			})
		}()
		<-firstStarted
		//: the call armed its delay ticker on the manual clock.
		manual.BlockUntil(1)
		//: one nanosecond short of the delay is not the delay.
		manual.Advance(c.delay - time.Nanosecond)
		if got := calls.Load(); got != 1 {
			t.Fatalf("%d copies before the delay elapsed, want 1", got)
		}
		manual.Advance(time.Nanosecond)
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v, want nil — the duplicate won", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("advancing the injected clock past Delay issued no duplicate")
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("%d copies in all, want 2", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
