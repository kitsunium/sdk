// Package resilience_test — the rate limiter as a caller configures it.
package resilience_test

import (
	"context"
	"math"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewRateLimiter pins the refusal of a non-finite rate, which is ADR 0031's
// fail-closed rule at its sharpest.
//
// The guard is finiteness, not sign, because IEEE-754 lets two values past a
// bare `<= 0` and both rebuild the inert policy the refusal exists to prevent.
// NaN compares false against every bound, poisons the credit on the first refill
// (x + NaN is NaN) and rejects every call forever. +Inf is worse: the refill term
// is elapsed × rate, so at exactly zero elapsed it is NaN and the call is
// rejected, and at any non-zero elapsed it is +Inf and the call is admitted —
// the same limiter admits everything or rejects everything according to clock
// granularity, and the admitting half fails OPEN.
//
// Neither arrives by a caller typing it: budget/window is +Inf for a zero
// window, and 0.0/0.0 is NaN.
func TestNewRateLimiter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  svcres.RateLimiterConfig
		//: whether every call must be refused as a misconfiguration.
		wantMisconfigured bool
	}
	tests := []tc{
		{name: "a plain rate", cfg: svcres.RateLimiterConfig{Rate: 10, Burst: 5}},
		{name: "a fractional rate", cfg: svcres.RateLimiterConfig{Rate: 0.5, Burst: 1}},
		{
			//: a non-positive burst still allows a single token, so the first
			//: caller is admitted rather than a limiter that admits nobody.
			name: "a zero burst clamps to one token",
			cfg:  svcres.RateLimiterConfig{Rate: 10, Burst: 0},
		},
		{name: "a zero rate is refused", cfg: svcres.RateLimiterConfig{Rate: 0, Burst: 1}, wantMisconfigured: true},
		{name: "a negative rate is refused", cfg: svcres.RateLimiterConfig{Rate: -1, Burst: 1}, wantMisconfigured: true},
		{
			name:              "a NaN rate is refused",
			cfg:               svcres.RateLimiterConfig{Rate: math.NaN(), Burst: 1},
			wantMisconfigured: true,
		},
		{
			name:              "a positive infinite rate is refused",
			cfg:               svcres.RateLimiterConfig{Rate: math.Inf(1), Burst: 1},
			wantMisconfigured: true,
		},
		{
			name:              "a negative infinite rate is refused",
			cfg:               svcres.RateLimiterConfig{Rate: math.Inf(-1), Burst: 1},
			wantMisconfigured: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewRateLimiter(c.cfg)

		ran := false
		err := r.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})

		if c.wantMisconfigured {
			//: the refusal names the policy as misconfigured rather than
			//: reporting RATE_LIMITED, which is what a working limiter says.
			if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
			}
			//: and it must not have run the operation.
			if ran {
				t.Error("a misconfigured limiter still ran the operation")
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if !ran {
			t.Error("the first caller was not admitted")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
