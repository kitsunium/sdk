package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/resilience"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// TestBreakerWindowThroughTheFacade pins the public path of #480: a window
// configured with the facade's types, the breaker read and forced through the
// BreakerState a caller asserts, and each change of phase heard.
func TestBreakerWindowThroughTheFacade(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	var heard []string
	b := resilience.NewCircuitBreaker(resilience.BreakerConfig{
		Clock:         clk,
		OpenDuration:  time.Minute,
		Window:        &resilience.BreakerWindowValue{MinCalls: 4, FailureRatio: 0.5},
		OnStateChange: func(from, to resilience.BreakerPhase) { heard = append(heard, from.String()+">"+to.String()) },
	})
	state, ok := b.(resilience.BreakerState)
	if !ok {
		t.Fatalf("%T is not a BreakerState", b)
	}
	down := errors.New("down")
	for i := range 4 {
		_ = b.Run(t.Context(), func(context.Context) error {
			if i%2 == 0 {
				return down
			}
			return nil
		})
	}
	if got := state.State(); got.Phase != resilience.BreakerOpen || got.Trips != 1 {
		t.Fatalf("after S/F alternating at a half: %+v, want open", got)
	}
	state.Reset()
	state.Isolate()
	clk.Advance(time.Hour)
	if err := b.Run(t.Context(), func(context.Context) error { return nil }); !errors.Is(err, resilience.CircuitOpen) {
		t.Fatalf("an isolated breaker = %v, want CircuitOpen", err)
	}
	want := fmt.Sprint([]string{"closed>open", "open>closed", "closed>open"})
	if got := fmt.Sprint(heard); got != want {
		t.Fatalf("heard %s, want %s", got, want)
	}
}
