package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/resilience"
)

// TestComposition wraps Retry(Timeout(op)) through the public facade.
func TestComposition(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		attempts int
		failures int
		wantErr  bool
		wantRuns int
	}
	tests := []tc{
		{"an operation that succeeds first time", 2, 0, false, 1},
		{"one failure inside the retry budget", 2, 1, false, 2},
		{"more failures than the budget allows", 2, 3, true, 2},
		{"a single-attempt policy does not retry", 1, 1, true, 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		retry := resilience.NewRetry(resilience.RetryConfig{MaxAttempts: c.attempts})
		timeout := resilience.NewTimeout(time.Second)

		runs := 0
		//: the composition is the point: Retry drives Timeout, and the inner
		//: policy must not swallow the error the outer one retries on.
		err := retry.Run(t.Context(), func(ctx context.Context) error {
			return timeout.Run(ctx, func(context.Context) error {
				runs++
				if runs <= c.failures {
					return errBoom
				}
				return nil
			})
		})
		if (err != nil) != c.wantErr {
			t.Errorf("composition error = %v, want error = %v", err, c.wantErr)
		}
		if runs != c.wantRuns {
			t.Errorf("operation ran %d times, want %d", runs, c.wantRuns)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// errBoom is the failure the composition test injects.
var errBoom = errors.New("boom")

// TestFallbackOverHedge exercises the two policies added on top of the original
// five through the public facade, composed the way they are meant to be: hedge
// the read for tail latency, and fall back to a cached answer if it fails
// anyway. It also pins that both refusals are visible from pkg/v1 — a zero
// HedgeConfig and a nil Fallback come back as PolicyMisconfigured rather than
// as a policy quietly doing nothing (ADR 0031).
func TestFallbackOverHedge(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the hedging config as the caller writes it — the zero value is the
		//: forgotten-everything case the policy has to refuse.
		hedge resilience.HedgeConfig
		//: what the hedged primary operation returns (nil = it succeeds).
		primaryErr error
		//: whether a fallback is configured at all. It doubles as the expected
		//: refusal: every failure below — including a refused hedge — is
		//: covered when a fallback exists, and surfaces when none does.
		withFallback bool
		//: expectations.
		wantFallbackRan bool
	}
	//: a delay no test operation can outlast, so nothing is ever duplicated
	//: here: this test is about composition, not about the race.
	sound := resilience.HedgeConfig{
		Idempotent:  true,
		Delay:       time.Hour,
		MaxHedges:   1,
		MaxInFlight: 4,
	}
	tests := []tc{
		{name: "the hedged read succeeds", hedge: sound, withFallback: true},
		{
			name:            "the hedged read fails and the cache answers",
			hedge:           sound,
			primaryErr:      errBoom,
			withFallback:    true,
			wantFallbackRan: true,
		},
		{
			//: the zero HedgeConfig duplicates nothing — it refuses. The
			//: refusal is a failure like any other, so the fallback covers it.
			name:            "an unconfigured hedge refuses and the cache answers",
			withFallback:    true,
			wantFallbackRan: true,
		},
		{
			//: nothing covers a fallback that has no fallback.
			name:  "a fallback with no fallback refuses",
			hedge: sound,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		hedge := resilience.NewHedge(c.hedge)

		fallbackRan := false
		cfg := resilience.FallbackConfig{}
		if c.withFallback {
			cfg.Fallback = func(context.Context) error {
				fallbackRan = true
				return nil
			}
		}
		fallback := resilience.NewFallback(cfg)

		err := fallback.Run(t.Context(), func(ctx context.Context) error {
			return hedge.Run(ctx, func(context.Context) error { return c.primaryErr })
		})

		if fallbackRan != c.wantFallbackRan {
			t.Errorf("the fallback ran = %v, want %v", fallbackRan, c.wantFallbackRan)
		}
		if !c.withFallback {
			//: uncovered, so the refusal reaches the caller as itself.
			if !errs.HasCode(err, resilience.PolicyMisconfigured.Code()) {
				t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
