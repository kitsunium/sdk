// Package resilience_test — the retry policy waiting on an injected clock.
package resilience_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestRetryWaitsOnItsClock pins that the retry backs off on the clock it is
// given and on nothing else: each attempt runs only once the manual clock has
// been moved past the exact backoff, and not a nanosecond before.
//
// Before RetryConfig.Clock the retry slept on a real timer, so a test of a
// retrying component either slept through every backoff or configured delays
// too small to mean anything.
//
// GOROUTINE LIFECYCLE: one goroutine runs the retry. It ends when its budget
// is spent, which the test drives by advancing the clock, and the test reads
// its result from done before returning, so nothing outlives the test.
func TestRetryWaitsOnItsClock(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	r := svcres.NewRetry(svcres.RetryConfig{MaxAttempts: 3, BaseDelay: time.Second, Clock: manual})
	var calls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- r.Run(t.Context(), func(context.Context) error {
			calls.Add(1)
			return errors.New("always fails")
		})
	}()

	//: the first attempt runs at once, then the loop arms a one-second wait.
	manual.BlockUntil(1)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d attempts before any time passed, want 1", got)
	}
	//: one nanosecond short of the backoff is not the backoff.
	manual.Advance(time.Second - time.Nanosecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("%d attempts before the first backoff elapsed, want 1", got)
	}
	manual.Advance(time.Nanosecond)
	//: the second attempt fails and arms the doubled wait.
	manual.BlockUntil(1)
	if got := calls.Load(); got != 2 {
		t.Fatalf("%d attempts after the first backoff, want 2", got)
	}
	manual.Advance(2 * time.Second)

	err := <-done
	if got := calls.Load(); got != 3 {
		t.Errorf("%d attempts in all, want the budget of 3", got)
	}
	if !kerrs.HasCode(err, coreres.CodeRetryExhausted) {
		t.Errorf("Run = %v, want RETRY_EXHAUSTED", err)
	}
}
