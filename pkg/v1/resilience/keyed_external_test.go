package resilience_test

import (
	"context"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/resilience"
)

// tenantKey is the context key the facade test charges calls to.
type tenantKey struct{}

// TestKeyedRateLimiterThroughTheFacade drives the keyed limiter the way a
// framework wires it — a Key reading the caller off the context, a clock the
// test moves — using only the public packages, so a consumer outside the
// module can build every argument it takes.
func TestKeyedRateLimiterThroughTheFacade(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	limiter := resilience.NewKeyedRateLimiter(resilience.KeyedRateLimiterConfig{
		Rate: 1, Burst: 1, MaxKeys: 10, IdleTimeout: time.Minute, Clock: manual,
		Key: func(ctx context.Context) string {
			tenant, _ := ctx.Value(tenantKey{}).(string)
			return tenant
		},
	})
	call := func(tenant string) error {
		ctx := context.WithValue(t.Context(), tenantKey{}, tenant)
		return limiter.Run(ctx, func(context.Context) error { return nil })
	}
	if err := call("a"); err != nil {
		t.Fatalf("first call of tenant a = %v", err)
	}
	if err := call("a"); !errs.HasReason(err, "RATE_LIMITED") {
		t.Fatalf("second call of tenant a = %v, want RATE_LIMITED", err)
	}
	if err := call("b"); err != nil {
		t.Errorf("tenant b paid for tenant a: %v", err)
	}
	manual.Advance(time.Second)
	if err := call("a"); err != nil {
		t.Errorf("tenant a after its refill = %v", err)
	}
}

// TestBackoffAndRetryShareOneCurve pins that the public Backoff and the retry
// policy wait the same durations for the same four fields: the retry is driven
// on a manual clock, and each attempt arrives exactly when Backoff.Delay says.
//
// GOROUTINE LIFECYCLE: one goroutine runs the retry; it ends when its budget
// of four attempts is spent, and the test reads its result from done before
// returning.
func TestBackoffAndRetryShareOneCurve(t *testing.T) {
	t.Parallel()
	curve := resilience.Backoff{BaseDelay: 100 * time.Millisecond, MaxDelay: 250 * time.Millisecond}
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	retry := resilience.NewRetry(resilience.RetryConfig{
		MaxAttempts: 4, BaseDelay: curve.BaseDelay, MaxDelay: curve.MaxDelay, Clock: manual,
	})
	attempts := make(chan time.Time, 4)
	done := make(chan error, 1)
	go func() {
		done <- retry.Run(t.Context(), func(context.Context) error {
			attempts <- manual.Now()
			return errBoom
		})
	}()
	previous := <-attempts
	for failure := 1; failure <= 3; failure++ {
		manual.BlockUntil(1)
		manual.Advance(curve.Delay(failure))
		at := <-attempts
		if waited := at.Sub(previous); waited != curve.Delay(failure) {
			t.Errorf("attempt %d waited %v, want Backoff.Delay(%d) = %v", failure+1, waited, failure, curve.Delay(failure))
		}
		previous = at
	}
	if err := <-done; !errs.HasReason(err, "RETRY_EXHAUSTED") {
		t.Errorf("Run = %v, want RETRY_EXHAUSTED", err)
	}
}
