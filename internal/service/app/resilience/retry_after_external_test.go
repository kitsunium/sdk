// Package resilience_test — a delay the failure imposes, read and honoured.
package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// throttled is an *errs.Error carrying fields — the shape every SDK producer
// of the delay returns.
func throttled(fields ...kerrs.FieldValue) error {
	return kerrs.Wrap(coreres.BulkheadFull, kerrs.WrapParams{}, fields...)
}

// TestRetryAfter pins what the helper reads: the retry_after_seconds field of
// the outermost *errs.Error, whole or fractional seconds, the outer word over
// an inner one, and nothing for a field that is absent, zero, negative or not
// a finite number.
func TestRetryAfter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		err    error
		want   time.Duration
		wantOK bool
	}
	tests := []tc{
		{name: "nil", err: nil},
		{name: "a plain error", err: errors.New("down")},
		{name: "an errs error without the field", err: throttled(kerrs.Int("status", 503))},
		{name: "whole seconds", err: throttled(kerrs.Int("retry_after_seconds", 7)), want: 7 * time.Second, wantOK: true},
		{name: "fractional seconds", err: throttled(kerrs.Float("retry_after_seconds", 0.25)), want: 250 * time.Millisecond, wantOK: true},
		{name: "a sub-nanosecond delay is not zero", err: throttled(kerrs.Float("retry_after_seconds", 1e-12)), want: time.Nanosecond, wantOK: true},
		{name: "zero is no hint", err: throttled(kerrs.Int("retry_after_seconds", 0))},
		{name: "a negative delay is no hint", err: throttled(kerrs.Int("retry_after_seconds", -1))},
		{name: "NaN is no hint", err: throttled(kerrs.Float("retry_after_seconds", math.NaN()))},
		{name: "+Inf is no hint", err: throttled(kerrs.Float("retry_after_seconds", math.Inf(1)))},
		{name: "text is no hint", err: throttled(kerrs.String("retry_after_seconds", "soon"))},
		{name: "past a Duration is the longest one", err: throttled(kerrs.Float("retry_after_seconds", 1e300)), want: math.MaxInt64, wantOK: true},
		{
			name: "the outer word wins",
			err: kerrs.Wrap(throttled(kerrs.Int("retry_after_seconds", 7)), kerrs.WrapParams{},
				kerrs.Int("retry_after_seconds", 3)),
			want: 3 * time.Second, wantOK: true,
		},
		{
			name: "under a stdlib wrapper",
			err:  fmt.Errorf("call: %w", throttled(kerrs.Int("retry_after_seconds", 2))),
			want: 2 * time.Second, wantOK: true,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := svcres.RetryAfter(c.err)
			if ok != c.wantOK || got != c.want {
				t.Fatalf("RetryAfter = %v, %v; want %v, %v", got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestRetryHonoursTheDelayTheFailureImposes pins RetryConfig.Delay on a manual
// clock: the second attempt waits exactly the delay the failure named — not
// the one-second curve, not a nanosecond less — and the third, whose failure
// names none, waits the curve.
//
// GOROUTINE LIFECYCLE: one goroutine runs the retry; the test drives every
// wait by advancing the clock and reads its result before returning.
func TestRetryHonoursTheDelayTheFailureImposes(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	type call struct {
		attempt int
		err     error
	}
	calls := make(chan call, 4)
	r := svcres.NewRetry(svcres.RetryConfig{
		MaxAttempts: 3, BaseDelay: time.Second, Clock: manual,
		Delay: func(attempt int, err error) (time.Duration, bool) {
			calls <- call{attempt, err}
			return svcres.RetryAfter(err)
		},
	})
	var attempts atomic.Int32
	first := throttled(kerrs.Int("retry_after_seconds", 5))
	done := make(chan error, 1)
	go func() {
		done <- r.Run(t.Context(), func(context.Context) error {
			//: the first failure names five seconds, the second names nothing.
			if attempts.Add(1) == 1 {
				return first
			}
			return errors.New("down")
		})
	}()

	manual.BlockUntil(1)
	if got := <-calls; got.attempt != 1 || got.err != first {
		t.Fatalf("Delay called with (%d, %v), want (1, the first failure)", got.attempt, got.err)
	}
	//: the curve would have fired at one second; the failure said five.
	manual.Advance(5*time.Second - time.Nanosecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("%d attempts before the imposed delay elapsed, want 1", got)
	}
	manual.Advance(time.Nanosecond)
	manual.BlockUntil(1)
	if got := (<-calls).attempt; got != 2 {
		t.Fatalf("Delay called for retry %d, want 2", got)
	}
	//: the second failure names nothing: the curve's two seconds.
	manual.Advance(2*time.Second - time.Nanosecond)
	if got := attempts.Load(); got != 2 {
		t.Fatalf("%d attempts before the curve's wait elapsed, want 2", got)
	}
	manual.Advance(time.Nanosecond)
	if err := <-done; !kerrs.HasCode(err, coreres.CodeRetryExhausted) {
		t.Fatalf("Run = %v, want RETRY_EXHAUSTED", err)
	}
}

// TestRetryHoldsAnImposedDelayAtMaxDelay pins the bound: a server asking for
// an hour gets MaxDelay, and a negative imposed delay is no wait at all.
func TestRetryHoldsAnImposedDelayAtMaxDelay(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		imposed time.Duration
		max     time.Duration
		want    time.Duration
	}
	tests := []tc{
		{name: "an hour held at ten seconds", imposed: time.Hour, max: 10 * time.Second, want: 10 * time.Second},
		{name: "no cap, the hour stands", imposed: time.Hour, want: time.Hour},
		{name: "negative is no wait", imposed: -time.Second, max: 10 * time.Second, want: 0},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
			r := svcres.NewRetry(svcres.RetryConfig{
				MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: c.max, Clock: manual,
				Delay: func(int, error) (time.Duration, bool) { return c.imposed, true },
			})
			var attempts atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- r.Run(t.Context(), func(context.Context) error {
					attempts.Add(1)
					return errors.New("down")
				})
			}()
			//: a zero wait fires at once, with no timer left pending.
			if c.want > 0 {
				manual.BlockUntil(1)
				manual.Advance(c.want - time.Nanosecond)
				if got := attempts.Load(); got != 1 {
					t.Fatalf("%d attempts before %v, want 1", got, c.want)
				}
				manual.Advance(time.Nanosecond)
			}
			if err := <-done; !kerrs.HasCode(err, coreres.CodeRetryExhausted) {
				t.Fatalf("Run = %v, want RETRY_EXHAUSTED", err)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("%d attempts, want 2", got)
			}
		})
	}
}

// TestRateLimitedSaysWhenToComeBack pins #476's limiter half: a rejection by
// either limiter carries the time its bucket needs to earn the next token,
// (1 − tokens) / rate, and RetryAfter reads it.
func TestRateLimitedSaysWhenToComeBack(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	limiters := map[string]func(clk clock.Clock) coreres.Runner{
		"NewRateLimiter": func(clk clock.Clock) coreres.Runner {
			return svcres.NewRateLimiter(svcres.RateLimiterConfig{Rate: 4, Burst: 1, Clock: clk})
		},
		"NewKeyedRateLimiter": func(clk clock.Clock) coreres.Runner {
			return svcres.NewKeyedRateLimiter(svcres.KeyedRateLimiterConfig{
				Rate: 4, Burst: 1, Clock: clk, MaxKeys: 8, IdleTimeout: time.Hour,
				Key: func(context.Context) string { return "client" },
			})
		},
	}
	ok := func(context.Context) error { return nil }
	for name, newLimiter := range limiters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manual := clock.NewManualClock(start)
			limiter := newLimiter(manual)
			if err := limiter.Run(t.Context(), ok); err != nil {
				t.Fatalf("the first call = %v, want it admitted", err)
			}
			//: an empty bucket at four tokens a second: a quarter of a second.
			err := limiter.Run(t.Context(), ok)
			if !kerrs.HasCode(err, coreres.CodeRateLimited) {
				t.Fatalf("the second call = %v, want RATE_LIMITED", err)
			}
			if got, found := svcres.RetryAfter(err); !found || got != 250*time.Millisecond {
				t.Fatalf("RetryAfter = %v, %v; want 250ms", got, found)
			}
			//: a tenth of a second later, three twentieths remain.
			manual.Advance(100 * time.Millisecond)
			err = limiter.Run(t.Context(), ok)
			if got, found := svcres.RetryAfter(err); !found || got != 150*time.Millisecond {
				t.Fatalf("RetryAfter = %v, %v; want 150ms", got, found)
			}
			//: the sentinel is still the origin: its status and reason stand.
			if kerrs.HTTPStatusOf(err) != 429 {
				t.Errorf("HTTPStatusOf = %d, want 429", kerrs.HTTPStatusOf(err))
			}
		})
	}
}
