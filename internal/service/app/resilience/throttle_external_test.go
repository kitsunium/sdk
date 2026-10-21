// Package resilience_test — the adaptive throttle (#482): Google SRE's
// client-side throttling, every refusal decided by an injected Random and
// every window moved on a ManualClock.
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

// errOverloaded is a dependency refusing a call.
var errOverloaded = errors.New("overloaded")

// draws is a Random that hands out the probability each call is compared to,
// and counts how often it was asked.
type draws struct {
	value float64
	asked int
}

// next is the Random.
func (d *draws) next() float64 {
	d.asked++
	return d.value
}

// newThrottle builds a throttle of cfg on a manual clock with a fixed draw.
func newThrottle(cfg svcres.AdaptiveThrottleConfig, draw float64) (coreres.Runner, *clock.ManualClock, *draws) {
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	d := &draws{value: draw}
	cfg.Clock, cfg.Random = manual, d.next
	return svcres.NewAdaptiveThrottle(cfg), manual, d
}

// TestAdaptiveThrottleNeverRefusesAHealthyDependency pins the healthy path: a
// dependency that accepts every call is never refused, and Random is never
// even asked.
func TestAdaptiveThrottleNeverRefusesAHealthyDependency(t *testing.T) {
	t.Parallel()
	th, _, d := newThrottle(svcres.AdaptiveThrottleConfig{}, 0)
	for call := range 1000 {
		if err := th.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
			t.Fatalf("call %d = %v, want admitted", call, err)
		}
	}
	if d.asked != 0 {
		t.Fatalf("Random asked %d times, want 0 on a healthy dependency", d.asked)
	}
}

// TestAdaptiveThrottleRefusesWithTheSREProbability pins the formula on a
// dependency refusing everything: the n-th call is compared to (n−1)/n —
// requests counted before it, no accepts — so with draws of 0.6 the first two
// calls run (probabilities 0 and 1/2) and the third (2/3) is refused with
// Throttled, without running.
func TestAdaptiveThrottleRefusesWithTheSREProbability(t *testing.T) {
	t.Parallel()
	th, _, _ := newThrottle(svcres.AdaptiveThrottleConfig{}, 0.6)
	ran := 0
	op := func(context.Context) error { ran++; return errOverloaded }
	for call := 1; call <= 2; call++ {
		if err := th.Run(t.Context(), op); !errors.Is(err, errOverloaded) {
			t.Fatalf("call %d = %v, want the dependency's refusal", call, err)
		}
	}
	err := th.Run(t.Context(), op)
	if !kerrs.HasCode(err, coreres.CodeThrottled) || ran != 2 {
		t.Fatalf("third call = %v (ran %d), want THROTTLED without running", err, ran)
	}
}

// TestAdaptiveThrottleLetsKRequestsPerAccept pins K: with K = 2 and one call
// in two accepted, requests never exceed twice the accepts and nothing is
// refused, even with a draw of 0 that refuses at any positive probability.
func TestAdaptiveThrottleLetsKRequestsPerAccept(t *testing.T) {
	t.Parallel()
	th, _, d := newThrottle(svcres.AdaptiveThrottleConfig{}, 0)
	for call := range 200 {
		var err error
		if call%2 == 0 {
			err = th.Run(t.Context(), func(context.Context) error { return nil })
		} else {
			err = th.Run(t.Context(), func(context.Context) error { return errOverloaded })
		}
		if kerrs.HasCode(err, coreres.CodeThrottled) {
			t.Fatalf("call %d refused (asked Random %d times), want none at one accept in two", call, d.asked)
		}
	}
}

// TestAdaptiveThrottleForgetsPastTheWindow pins the window: once the refusals
// are older than Window, the counts are empty and the next call runs.
func TestAdaptiveThrottleForgetsPastTheWindow(t *testing.T) {
	t.Parallel()
	th, manual, _ := newThrottle(svcres.AdaptiveThrottleConfig{Window: time.Minute, Buckets: 6}, 0)
	op := func(context.Context) error { return errOverloaded }
	_ = th.Run(t.Context(), op)
	if err := th.Run(t.Context(), op); !kerrs.HasCode(err, coreres.CodeThrottled) {
		t.Fatalf("second call = %v, want THROTTLED after a refusal", err)
	}
	manual.Advance(time.Minute)
	if err := th.Run(t.Context(), op); !errors.Is(err, errOverloaded) {
		t.Fatalf("call a window later = %v, want it sent to the dependency", err)
	}
}

// TestAdaptiveThrottleCountsWhatRejectedCallsAnAnswer pins the classifier: an
// error Rejected does not call a refusal counts as an accept, so a stream of
// them is never throttled.
func TestAdaptiveThrottleCountsWhatRejectedCallsAnAnswer(t *testing.T) {
	t.Parallel()
	errNotFound := errors.New("not found")
	th, _, _ := newThrottle(svcres.AdaptiveThrottleConfig{
		Rejected: func(err error) bool { return errors.Is(err, errOverloaded) },
	}, 0)
	for call := range 100 {
		if err := th.Run(t.Context(), func(context.Context) error { return errNotFound }); !errors.Is(err, errNotFound) {
			t.Fatalf("call %d = %v, want the answer, never THROTTLED", call, err)
		}
	}
}

// TestAdaptiveThrottleCountsACallerWhoLeftAsAnAccept pins #477's rule here:
// a context.Canceled returned once the caller's context is closed says
// nothing about the dependency, and never builds up refusals.
func TestAdaptiveThrottleCountsACallerWhoLeftAsAnAccept(t *testing.T) {
	t.Parallel()
	th, _, _ := newThrottle(svcres.AdaptiveThrottleConfig{}, 0)
	for call := range 100 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := th.Run(ctx, func(ctx context.Context) error { return ctx.Err() })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d = %v, want context.Canceled, never THROTTLED", call, err)
		}
	}
}

// TestAdaptiveThrottleRefusesAConfigItCannotHonour pins the refusals, each
// naming its field, and the defaults the zeros take.
func TestAdaptiveThrottleRefusesAConfigItCannotHonour(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cfg  svcres.AdaptiveThrottleConfig
		knob string
	}{
		{svcres.AdaptiveThrottleConfig{K: 0.5}, "K"},
		{svcres.AdaptiveThrottleConfig{K: math.NaN()}, "K"},
		{svcres.AdaptiveThrottleConfig{K: math.Inf(1)}, "K"},
		{svcres.AdaptiveThrottleConfig{Window: -time.Second}, "Window"},
		{svcres.AdaptiveThrottleConfig{Window: 5, Buckets: 10}, "Window"},
		{svcres.AdaptiveThrottleConfig{Buckets: -1}, "Buckets"},
		{svcres.AdaptiveThrottleConfig{Buckets: 1001}, "Buckets"},
		{svcres.AdaptiveThrottleConfig{K: 1, Window: time.Second, Buckets: 1000}, ""},
		{svcres.AdaptiveThrottleConfig{}, ""},
	}
	for _, c := range cases {
		err := svcres.NewAdaptiveThrottle(c.cfg).Run(t.Context(), func(context.Context) error { return nil })
		if c.knob == "" {
			if err != nil {
				t.Fatalf("%+v: Run = %v, want admitted", c.cfg, err)
			}
			continue
		}
		if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) || knobOf(err) != c.knob {
			t.Fatalf("%+v: Run = %v, want POLICY_MISCONFIGURED naming %s", c.cfg, err, c.knob)
		}
	}
}
