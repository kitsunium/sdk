// Package resilience_test — RetryConfig.JitterMode (#482): full and
// decorrelated jitter, each wait observed through OnRetry and run out on a
// ManualClock.
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// retryWaits runs a retry of cfg over an operation that always fails and
// returns every wait OnRetry heard, each run out on the manual clock.
//
// GOROUTINE LIFECYCLE: the retry runs on one goroutine until its attempts
// are spent; the test drives each wait and joins it through done.
func retryWaits(t *testing.T, cfg svcres.RetryConfig) []time.Duration {
	t.Helper()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	heard := make(chan time.Duration, cfg.MaxAttempts)
	cfg.Clock = manual
	cfg.OnRetry = func(_ int, _ error, delay time.Duration) { heard <- delay }
	r := svcres.NewRetry(cfg)
	done := make(chan error, 1)
	go func() { done <- r.Run(t.Context(), func(context.Context) error { return errors.New("down") }) }()
	waits := make([]time.Duration, 0, cfg.MaxAttempts-1)
	for range cfg.MaxAttempts - 1 {
		delay := <-heard
		waits = append(waits, delay)
		//: a zero wait fires as it is armed: there is nothing to run out.
		if delay > 0 {
			manual.BlockUntil(1)
			manual.Advance(delay)
		}
	}
	<-done
	return waits
}

// TestRetryFullJitterWaitsBelowTheCurve pins JitterFull: every wait lies in
// [0, the capped curve], and the Jitter fraction is not added on top.
func TestRetryFullJitterWaitsBelowTheCurve(t *testing.T) {
	t.Parallel()
	cfg := svcres.RetryConfig{
		MaxAttempts: 8, BaseDelay: time.Second, MaxDelay: 10 * time.Second,
		Jitter: 1, JitterMode: kbackoff.JitterFull,
	}
	curve := kbackoff.Value{BaseDelay: cfg.BaseDelay, MaxDelay: cfg.MaxDelay}
	for round := range 20 {
		for i, wait := range retryWaits(t, cfg) {
			if ceiling := curve.Delay(i + 1); wait < 0 || wait > ceiling {
				t.Fatalf("round %d retry %d waited %v, want within [0, %v]", round, i+1, wait, ceiling)
			}
		}
	}
}

// TestRetryDecorrelatedJitterGrowsFromTheWaitBefore pins JitterDecorrelated:
// each wait lies in [BaseDelay, min(MaxDelay, 3 × the wait before)], the
// first one drawn from [BaseDelay, 3 × BaseDelay].
func TestRetryDecorrelatedJitterGrowsFromTheWaitBefore(t *testing.T) {
	t.Parallel()
	cfg := svcres.RetryConfig{
		MaxAttempts: 8, BaseDelay: time.Second, MaxDelay: 20 * time.Second,
		JitterMode: kbackoff.JitterDecorrelated,
	}
	for round := range 20 {
		previous := cfg.BaseDelay
		for i, wait := range retryWaits(t, cfg) {
			upper := min(3*previous, cfg.MaxDelay)
			if wait < cfg.BaseDelay || wait > upper {
				t.Fatalf("round %d retry %d waited %v, want within [%v, %v]", round, i+1, wait, cfg.BaseDelay, upper)
			}
			previous = wait
		}
	}
}

// TestRetryDecorrelatedJitterIgnoresAnImposedWait pins that a wait the
// failure imposed through Delay is not the previous wait a decorrelated draw
// grows from: with every second retry imposed at an hour, the drawn waits
// stay within three times the last DRAWN one.
func TestRetryDecorrelatedJitterIgnoresAnImposedWait(t *testing.T) {
	t.Parallel()
	cfg := svcres.RetryConfig{
		MaxAttempts: 9, BaseDelay: time.Second, MaxDelay: 2 * time.Hour,
		JitterMode: kbackoff.JitterDecorrelated,
		Delay: func(attempt int, _ error) (time.Duration, bool) {
			return time.Hour, attempt%2 == 0
		},
	}
	previous := cfg.BaseDelay
	for i, wait := range retryWaits(t, cfg) {
		if (i+1)%2 == 0 {
			if wait != time.Hour {
				t.Fatalf("retry %d waited %v, want the imposed hour", i+1, wait)
			}
			continue
		}
		if upper := 3 * previous; wait < cfg.BaseDelay || wait > upper {
			t.Fatalf("retry %d waited %v, want within [%v, %v]", i+1, wait, cfg.BaseDelay, upper)
		}
		previous = wait
	}
}
