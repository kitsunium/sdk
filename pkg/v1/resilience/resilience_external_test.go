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

// TestRejectionsCarryTheirHTTPStatus pins what a framework mapping errors to
// responses reads through errs.HTTPStatusOf: a rejection says what a client
// should do — 429 slow down, 503 try later, 504 the answer did not come —
// where a bare 500 would say the server is broken. Each case produces the
// verdict through a real policy, not by naming the sentinel.
//
// Goroutine lifecycle: the bulkhead case starts exactly one goroutine to hold
// the only slot, and joins it on its done channel before the case returns.
func TestRejectionsCarryTheirHTTPStatus(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		reject func(t *testing.T) error
		want   int
	}
	tests := []tc{
		{"a rate limiter out of tokens", func(t *testing.T) error {
			limiter := resilience.NewRateLimiter(resilience.RateLimiterConfig{Rate: 0.001, Burst: 1})
			if err := limiter.Run(t.Context(), noop); err != nil {
				t.Fatalf("the first call spent the burst and failed: %v", err)
			}
			return limiter.Run(t.Context(), noop)
		}, 429},
		{"a full bulkhead", func(t *testing.T) error {
			bulkhead := resilience.NewBulkhead(1)
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- bulkhead.Run(t.Context(), func(context.Context) error {
					close(entered)
					<-release
					return nil
				})
			}()
			<-entered
			err := bulkhead.Run(t.Context(), noop)
			close(release)
			if holderErr := <-done; holderErr != nil {
				t.Fatalf("the slot holder failed: %v", holderErr)
			}
			return err
		}, 503},
		{"an open circuit", func(t *testing.T) error {
			breaker := resilience.NewCircuitBreaker(resilience.BreakerConfig{FailureThreshold: 1, OpenDuration: time.Hour})
			if err := breaker.Run(t.Context(), func(context.Context) error { return errBoom }); !errors.Is(err, errBoom) {
				t.Fatalf("the tripping call = %v, want the operation's own error", err)
			}
			return breaker.Run(t.Context(), noop)
		}, 503},
		{"an operation past its timeout", func(t *testing.T) error {
			return resilience.NewTimeout(time.Millisecond).Run(t.Context(), func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			})
		}, 504},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := c.reject(t)
		if err == nil {
			t.Fatalf("%s: the policy admitted the call", c.name)
		}
		if got := errs.HTTPStatusOf(err); got != c.want {
			t.Errorf("%s: HTTPStatusOf(%v) = %d, want %d", c.name, err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the verdicts that are not rejections keep the 500 a failure is.
	for _, sentinel := range []error{resilience.RetryExhausted, resilience.FallbackFailed, resilience.PolicyMisconfigured} {
		if got := errs.HTTPStatusOf(sentinel); got != 500 {
			t.Errorf("HTTPStatusOf(%v) = %d, want 500", sentinel, got)
		}
	}
}

// noop is an operation that succeeds at once.
func noop(context.Context) error {
	return nil
}
