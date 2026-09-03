package resilience

import (
	"context"
	"testing"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_newMisconfigured pins the refusal Runner's two obligations: it never runs
// the operation, and it names the policy and the knob without echoing the value
// that was rejected (ADR 0031).
func Test_newMisconfigured(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		policy string
		knob   string
	}
	tests := []tc{
		{"rate limiter refusal", "ratelimit", "Rate"},
		{"timeout refusal", "timeout", "duration"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ran := false
			err := newMisconfigured(tc.policy, tc.knob).Run(t.Context(),
				func(context.Context) error { ran = true; return nil })
			//: fail-closed: running the work unguarded is the banned behaviour.
			if ran {
				t.Error("the operation ran; a refused policy must not execute it")
			}
			//: the refusal is matchable by code, not by string comparison.
			if !errs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("err=%v, want POLICY_MISCONFIGURED", err)
			}
			//: the fields identify the offender for the operator.
			msg := err.Error()
			if msg == "" {
				t.Error("empty error message")
			}
		})
	}
}

// Test_misconfiguredRunner_Run is the name-matched test for the Run method
// (KTN-TEST-SYNC/COVERAGE). It pins the two properties the refusal exists for:
// the operation is never executed, and the error is the permanent
// PolicyMisconfigured rather than any of the transient policy outcomes a caller
// might reasonably retry.
func Test_misconfiguredRunner_Run(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: a transient sentinel the refusal must NOT be confused with.
		notCode errs.Code
	}
	tests := []tc{
		{"is not a rate-limit rejection", coreres.CodeRateLimited},
		{"is not a deadline overrun", coreres.CodeTimeoutExceeded},
		{"is not an open circuit", coreres.CodeCircuitOpen},
		{"is not an exhausted retry budget", coreres.CodeRetryExhausted},
	}
	runner := newMisconfigured("ratelimit", "Rate")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ran := false
			err := runner.Run(t.Context(), func(context.Context) error { ran = true; return nil })
			//: fail-closed on every call, not just the first.
			if ran {
				t.Error("the operation ran; a refused policy must not execute it")
			}
			//: the refusal is always the permanent code…
			if !errs.HasCode(err, coreres.CodePolicyMisconfigured) {
				t.Fatalf("err=%v, want POLICY_MISCONFIGURED", err)
			}
			//: …and never a transient one, which is what makes a broken policy
			//: distinguishable from a working one at the call site.
			if errs.HasCode(err, tc.notCode) {
				t.Errorf("err=%v also carries a transient sentinel", err)
			}
		})
	}
}
