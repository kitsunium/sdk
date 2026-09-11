// Package resilience_test — the fallback policy as a caller configures it.
package resilience_test

import (
	"context"
	"errors"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/resilience"
)

// TestNewFallback pins the refusal of a nil Fallback, and why it is a refusal
// rather than a default.
//
// The secondary operation IS this policy. Both values the SDK could invent are
// worse than saying so: running the primary and passing its error through is a
// policy that does nothing while the caller believes plan B is wired up, and a
// no-op fallback returning nil would report success for work that never ran.
func TestNewFallback(t *testing.T) {
	t.Parallel()
	secondary := func(context.Context) error { return nil }

	type tc struct {
		name string
		cfg  svcres.FallbackConfig
		//: whether every call must be refused as a misconfiguration.
		wantMisconfigured bool
	}
	tests := []tc{
		{name: "a configured fallback", cfg: svcres.FallbackConfig{Fallback: secondary}},
		{
			name: "a configured fallback with a classifier",
			cfg: svcres.FallbackConfig{
				Fallback:  secondary,
				Retryable: func(error) bool { return true },
			},
		},
		{name: "a nil fallback is refused", cfg: svcres.FallbackConfig{}, wantMisconfigured: true},
		{
			name:              "a nil fallback with a classifier is still refused",
			cfg:               svcres.FallbackConfig{Retryable: func(error) bool { return true }},
			wantMisconfigured: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		r := svcres.NewFallback(c.cfg)

		ran := false
		err := r.Run(t.Context(), func(context.Context) error {
			ran = true
			return nil
		})

		if c.wantMisconfigured {
			//: the refusal names the policy as misconfigured rather than
			//: reporting the composed failure a working fallback would.
			if !kerrs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("Run = %v, want POLICY_MISCONFIGURED", err)
			}
			//: and it must not have run the primary operation either.
			if ran {
				t.Error("a misconfigured fallback still ran the operation")
			}
			return
		}
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if !ran {
			t.Error("the primary operation did not run")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFallbackAbsorbsThePrimaryFailure pins the policy's whole purpose from the
// caller's side: when plan B works, Run reports success and the primary error
// does not surface. That masking is deliberate — a caller who needs to know how
// often plan B fires instruments the fallback Operation, which is their own
// closure.
func TestFallbackAbsorbsThePrimaryFailure(t *testing.T) {
	t.Parallel()
	primaryErr := errors.New("the primary failed")

	fallbackRuns := 0
	r := svcres.NewFallback(svcres.FallbackConfig{
		Fallback: func(context.Context) error {
			fallbackRuns++
			return nil
		},
	})

	if err := r.Run(t.Context(), func(context.Context) error { return primaryErr }); err != nil {
		t.Fatalf("Run = %v, want nil — a successful fallback masks the primary error", err)
	}
	if fallbackRuns != 1 {
		t.Errorf("the fallback ran %d times, want exactly 1", fallbackRuns)
	}
	//: and a success on the primary path must leave plan B untouched.
	if err := r.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if fallbackRuns != 1 {
		t.Errorf("the fallback ran %d times, want it untouched by a successful primary", fallbackRuns)
	}
}
