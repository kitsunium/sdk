package kit_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// A service whose hand-written loop waits on a ticker, the way a Go program
// does — but on the app's clock.

var Ticks = kit.NewService("ticks", "A hand-written loop on the app's clock, for the tests.")

// ticked receives every instant the loop saw.
var ticked = make(chan time.Time, 8)

var Ticking = Ticks.Go("ticking", tickHourly)

func tickHourly(ctx context.Context) error {
	t := kit.NewTicker(ctx, time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case at, open := <-t.C():
			if !open {
				return nil
			}
			ticked <- at
		}
	}
}

// A time.Ticker in a hand-written loop ticks on the wall clock whatever a
// test does. kit.NewTicker ticks on the app's: a manual clock moved by an hour
// makes the loop run, with the manual clock's time.
func TestAHandWrittenLoopTicksOnTheAppClock(t *testing.T) {
	start := time.Date(2031, 1, 1, 9, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	app := kit.NewApp("ticks", Ticks).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard), kit.Clock(clk))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})

	// The loop's goroutine may not have made its ticker yet: move the clock an
	// hour at a time until it ticks.
	deadline := time.Now().Add(5 * time.Second)
	for {
		clk.Advance(time.Hour)
		select {
		case at, open := <-ticked:
			if !open {
				t.Fatal("the loop's channel closed")
			}
			if at.Before(start.Add(time.Hour)) || at.After(clk.Now()) {
				t.Fatalf("the loop ticked at %s, not on the manual clock (%s → %s)", at, start, clk.Now())
			}
			return
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the loop never ticked: its ticker is not on the app's clock")
		}
	}
}
