// Package resilience_test — the circuit breaker's sliding window: a rate of
// failures or of slow calls, on the breaker's clock (#480).
package resilience_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// epoch is the instant every manual clock of these tests starts at.
var epoch = time.Unix(1_700_000_000, 0)

// succeed and fail are the two outcomes a window counts.
func succeed(context.Context) error { return nil }

// fail is a transient failure.
func fail(context.Context) error { return errDown }

// takes returns an operation that lasts d on clk, then returns err.
func takes(clk *clock.ManualClock, d time.Duration, err error) coreres.Operation {
	return func(context.Context) error {
		clk.Advance(d)
		return err
	}
}

// stateOf is the BreakerState of a Runner NewCircuitBreaker built.
func stateOf(t *testing.T, r coreres.Runner) coreres.BreakerState {
	t.Helper()
	s, ok := r.(coreres.BreakerState)
	if !ok {
		t.Fatalf("%T is not a BreakerState", r)
	}
	return s
}

// rejected reports whether a breaker refuses the next call fast.
func rejected(b coreres.Runner) bool {
	ran, err := probe(b)
	return !ran && kerrs.HasCode(err, coreres.CodeCircuitOpen)
}

// TestBreakerWindowOpensOnARateAStreakMisses pins the defect the window
// exists for: S, F, S, F… never reaches a consecutive threshold, however long
// it runs, and a failure rate of one half does.
func TestBreakerWindowOpensOnARateAStreakMisses(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	streak := svcres.NewCircuitBreaker(svcres.BreakerConfig{FailureThreshold: 2, Clock: clk})
	window := svcres.NewCircuitBreaker(svcres.BreakerConfig{Clock: clk, Window: &svcres.BreakerWindowValue{
		MinCalls: 10, FailureRatio: 0.5,
	}})
	for i := range 10 {
		op := coreres.Operation(succeed)
		if i%2 == 1 {
			op = fail
		}
		_ = streak.Run(t.Context(), op)
		_ = window.Run(t.Context(), op)
	}
	if rejected(streak) {
		t.Error("the consecutive breaker opened on an alternation it cannot see")
	}
	if !rejected(window) {
		t.Errorf("the window did not open at 5 failures in 10 calls: %+v", stateOf(t, window).State())
	}
}

// TestBreakerWindowWaitsForMinCalls pins the throughput floor: below MinCalls
// one failure would be a rate of 100%, so no rate is read at all.
func TestBreakerWindowWaitsForMinCalls(t *testing.T) {
	t.Parallel()
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		Clock:  clock.NewManualClock(epoch),
		Window: &svcres.BreakerWindowValue{MinCalls: 5, FailureRatio: 0.5},
	})
	for range 4 {
		_ = b.Run(t.Context(), fail)
	}
	if rejected(b) {
		t.Fatal("four failures opened a window that needs five calls")
	}
	//: the probe above was a success: 4 failures of 5 calls.
	if got := stateOf(t, b).State(); got.Phase != coreres.BreakerOpen || got.Trips != 1 {
		t.Fatalf("after the fifth call: %+v, want open once", got)
	}
}

// TestBreakerWindowSlides pins the window's reach: a failure older than Size
// has left it, slot by slot, on the breaker's clock.
func TestBreakerWindowSlides(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		after    time.Duration
		wantOpen bool
	}
	tests := []tc{
		{"a failure inside the window counts", 5 * time.Second, true},
		{"its slot still in the window counts", 9 * time.Second, true},
		{"a failure a window old has left it", 10 * time.Second, false},
		{"a failure far older has left it", time.Hour, false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			b := svcres.NewCircuitBreaker(svcres.BreakerConfig{Clock: clk, Window: &svcres.BreakerWindowValue{
				Size: 10 * time.Second, Buckets: 10, MinCalls: 2, FailureRatio: 0.5,
			}})
			_ = b.Run(t.Context(), fail)
			clk.Advance(c.after)
			_ = b.Run(t.Context(), succeed)
			if got := rejected(b); got != c.wantOpen {
				t.Errorf("open = %v, want %v: %+v", got, c.wantOpen, stateOf(t, b).State())
			}
		})
	}
}

// TestBreakerWindowCountsSlowCalls pins the slow-call criterion: a call that
// lasts SlowAfter or longer on the breaker's clock is slow, succeeded or not,
// and a SlowRatio of them opens the breaker though none failed.
func TestBreakerWindowCountsSlowCalls(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		took     time.Duration
		wantSlow int
		wantOpen bool
	}
	tests := []tc{
		{"one tick short is not slow", time.Second - time.Nanosecond, 0, false},
		{"SlowAfter itself is slow", time.Second, 3, true},
		{"longer is slow", time.Minute, 3, true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			b := svcres.NewCircuitBreaker(svcres.BreakerConfig{Clock: clk, Window: &svcres.BreakerWindowValue{
				Size: time.Hour, MinCalls: 4, SlowAfter: time.Second, SlowRatio: 0.75,
			}})
			for range 3 {
				_ = b.Run(t.Context(), takes(clk, c.took, nil))
			}
			if got := stateOf(t, b).State(); got.SlowCalls != c.wantSlow || got.Failures != 0 || got.Calls != 3 {
				t.Fatalf("after three calls: %+v, want %d slow of 3, none failed", got, c.wantSlow)
			}
			_ = b.Run(t.Context(), succeed)
			if got := rejected(b); got != c.wantOpen {
				t.Errorf("open = %v, want %v", got, c.wantOpen)
			}
		})
	}
}

// TestBreakerWindowSlowTrialReopens pins the HalfOpen trial under a window: a
// trial that answers, but SlowAfter late, says the dependency is still slow.
func TestBreakerWindowSlowTrialReopens(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(epoch)
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{Clock: clk, OpenDuration: time.Minute, Window: &svcres.BreakerWindowValue{
		MinCalls: 1, SlowAfter: time.Second,
	}})
	state := stateOf(t, b)
	_ = b.Run(t.Context(), fail)
	clk.Advance(time.Minute)
	if err := b.Run(t.Context(), takes(clk, time.Second, nil)); err != nil {
		t.Fatalf("the slow trial = %v, want its own nil", err)
	}
	if got := state.State(); got.Phase != coreres.BreakerOpen || got.Trips != 2 {
		t.Fatalf("after a slow trial: %+v, want open again", got)
	}
	clk.Advance(time.Minute)
	if err := b.Run(t.Context(), takes(clk, time.Second-time.Nanosecond, nil)); err != nil {
		t.Fatalf("the prompt trial = %v", err)
	}
	if got := state.State(); got.Phase != coreres.BreakerClosed {
		t.Fatalf("after a prompt trial: %+v, want closed", got)
	}
}

// TestBreakerWindowLeavesNeutralCallsOut pins that a deterministic error and a
// caller who left are not calls of the window: they would dilute its rate.
func TestBreakerWindowLeavesNeutralCallsOut(t *testing.T) {
	t.Parallel()
	errBad := errors.New("bad request")
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		Clock:     clock.NewManualClock(epoch),
		Retryable: func(err error) bool { return !errors.Is(err, errBad) },
		Window:    &svcres.BreakerWindowValue{MinCalls: 1},
	})
	_ = b.Run(t.Context(), func(context.Context) error { return errBad })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = b.Run(ctx, func(ctx context.Context) error { return ctx.Err() })
	if got := stateOf(t, b).State(); got.Calls != 0 || got.Phase != coreres.BreakerClosed {
		t.Fatalf("after two neutral calls: %+v, want an empty window", got)
	}
}

// TestBreakerWindowDefaults pins the published defaults a zero Window takes:
// Polly's MinimumThroughput 100 and FailureRatio 0.1.
func TestBreakerWindowDefaults(t *testing.T) {
	t.Parallel()
	b := svcres.NewCircuitBreaker(svcres.BreakerConfig{
		Clock: clock.NewManualClock(epoch), Window: &svcres.BreakerWindowValue{},
	})
	for range 90 {
		_ = b.Run(t.Context(), succeed)
	}
	for range 9 {
		_ = b.Run(t.Context(), fail)
	}
	if got := stateOf(t, b).State(); got.Phase != coreres.BreakerClosed || got.Calls != 99 {
		t.Fatalf("at 99 calls: %+v, want closed below MinimumThroughput", got)
	}
	_ = b.Run(t.Context(), fail)
	if got := stateOf(t, b).State(); got.Phase != coreres.BreakerOpen {
		t.Fatalf("at 10 failures in 100 calls: %+v, want open", got)
	}
}

// TestBreakerWindowRefusesWhatItCannotHonour pins ADR 0031 on every field: a
// value out of range is refused, naming the field, the operation never runs,
// and the Runner is not a BreakerState.
func TestBreakerWindowRefusesWhatItCannotHonour(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      svcres.BreakerConfig
		wantKnob string
	}
	window := func(w svcres.BreakerWindowValue) svcres.BreakerConfig {
		return svcres.BreakerConfig{Window: &w}
	}
	tests := []tc{
		{"a negative Size", window(svcres.BreakerWindowValue{Size: -time.Second}), "Window.Size"},
		{"a negative Buckets", window(svcres.BreakerWindowValue{Buckets: -1}), "Window.Buckets"},
		{"too many Buckets", window(svcres.BreakerWindowValue{Buckets: 1001}), "Window.Buckets"},
		{"Buckets of math.MaxInt", window(svcres.BreakerWindowValue{Buckets: math.MaxInt}), "Window.Buckets"},
		{"a Size too short to split", window(svcres.BreakerWindowValue{Size: 5, Buckets: 10}), "Window.Size"},
		{"a negative MinCalls", window(svcres.BreakerWindowValue{MinCalls: -1}), "Window.MinCalls"},
		{"a negative FailureRatio", window(svcres.BreakerWindowValue{FailureRatio: -0.1}), "Window.FailureRatio"},
		{"a FailureRatio above one", window(svcres.BreakerWindowValue{FailureRatio: 1.5}), "Window.FailureRatio"},
		{"a NaN FailureRatio", window(svcres.BreakerWindowValue{FailureRatio: math.NaN()}), "Window.FailureRatio"},
		{"an infinite FailureRatio", window(svcres.BreakerWindowValue{FailureRatio: math.Inf(1)}), "Window.FailureRatio"},
		{"a negative SlowAfter", window(svcres.BreakerWindowValue{SlowAfter: -time.Second}), "Window.SlowAfter"},
		{"a SlowRatio above one", window(svcres.BreakerWindowValue{SlowRatio: 2}), "Window.SlowRatio"},
		{"a NaN SlowRatio", window(svcres.BreakerWindowValue{SlowRatio: math.NaN()}), "Window.SlowRatio"},
		{"a zero OpenBackoff base", svcres.BreakerConfig{OpenBackoff: &svcres.BackoffValue{MaxDelay: time.Minute}}, "OpenBackoff.BaseDelay"},
		{"no OpenBackoff ceiling", svcres.BreakerConfig{OpenBackoff: &svcres.BackoffValue{BaseDelay: time.Second}}, "OpenBackoff.MaxDelay"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := svcres.NewCircuitBreaker(c.cfg)
		ran, err := probe(b)
		if ran || !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
			t.Fatalf("ran=%v err=%v, want PolicyMisconfigured without running", ran, err)
		}
		if got := knobOf(err); got != c.wantKnob {
			t.Errorf("knob = %q, want %q", got, c.wantKnob)
		}
		if _, ok := b.(coreres.BreakerState); ok {
			t.Error("a refused breaker claims a state it does not keep")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBreakerWindowAcceptsItsBounds pins the edges that ARE honoured: a ratio
// of exactly one, a thousand buckets, one nanosecond per slot.
func TestBreakerWindowAcceptsItsBounds(t *testing.T) {
	t.Parallel()
	for _, w := range []svcres.BreakerWindowValue{
		{FailureRatio: 1, SlowRatio: 1},
		{Buckets: 1000},
		{Size: 10, Buckets: 10},
		{Buckets: 1},
	} {
		b := svcres.NewCircuitBreaker(svcres.BreakerConfig{Window: &w})
		if ran, err := probe(b); !ran || err != nil {
			t.Errorf("%+v: ran=%v err=%v, want an honoured window", w, ran, err)
		}
	}
}
