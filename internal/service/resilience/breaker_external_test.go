// Package resilience_test — the circuit breaker as a caller configures it.
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewCircuitBreaker pins the defaults, and specifically the one that ADR
// 0031 exists for.
//
// A zero OpenDuration admits the very next call, which is a breaker that rejects
// nothing — the caller believes they are protected and are not. It is clamped to
// a conventional cooldown rather than honoured, because "no cooldown" is never
// what a caller who configured a breaker meant.
func TestNewCircuitBreaker(t *testing.T) {
	t.Parallel()
	failure := errors.New("down")

	type tc struct {
		name string
		cfg  svcres.BreakerConfig
		//: how many failures it takes to see the first rejection.
		wantTripAfter int
	}
	tests := []tc{
		{
			name:          "an explicit threshold",
			cfg:           svcres.BreakerConfig{FailureThreshold: 2, OpenDuration: time.Minute},
			wantTripAfter: 2,
		},
		{
			//: a non-positive threshold falls back to the five-failure default.
			name:          "a zero threshold defaults",
			cfg:           svcres.BreakerConfig{OpenDuration: time.Minute},
			wantTripAfter: 5,
		},
		{
			name:          "a negative threshold defaults",
			cfg:           svcres.BreakerConfig{FailureThreshold: -3, OpenDuration: time.Minute},
			wantTripAfter: 5,
		},
		{
			//: the cooldown is clamped, so the breaker still rejects after the
			//: threshold rather than admitting the next call immediately.
			name:          "a zero cooldown is clamped",
			cfg:           svcres.BreakerConfig{FailureThreshold: 2},
			wantTripAfter: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := svcres.NewCircuitBreaker(c.cfg)

		//: drive it to the trip point.
		for i := range c.wantTripAfter {
			err := b.Run(t.Context(), func(context.Context) error { return failure })
			//: it must not reject before the threshold is reached.
			if kerrs.HasCode(err, coreres.CodeCircuitOpen) {
				t.Fatalf("the breaker opened after %d failures, want %d", i, c.wantTripAfter)
			}
		}

		//: the next call must be rejected without running.
		ran := false
		err := b.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})
		if !kerrs.HasCode(err, coreres.CodeCircuitOpen) {
			t.Fatalf("the breaker did not open after %d failures: %v", c.wantTripAfter, err)
		}
		if ran {
			t.Error("an open breaker still ran the operation")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
