// Package gate_test exercises the facade the way a consumer does: through the
// public package alone.
//
// The restriction is the assertion. A test that reached the internal layers
// would pass against a facade missing half its surface, which is how
// pkg/v1/entitlement shipped fifteen codes and not one sentinel.
package gate_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/entitlement"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/gate"
)

// shipped is a policy shaped like a real product's.
func shipped() *gate.Policy {
	return &gate.Policy{
		ExemptExact:      []string{"", "version", "help", "upgrade", "skill"},
		ExemptSubtree:    []string{"license"},
		RecoveryPaths:    []string{"upgrade", "license create"},
		OnUpdateRequired: gate.UpdateApply,
	}
}

// TestAConsumerCanBuildAndValidateAPolicy pins that everything needed to
// construct and check a policy is reachable from the public package.
func TestAConsumerCanBuildAndValidateAPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy *gate.Policy
		wantOK bool
		reason string
	}{
		{
			name: "a shipped policy", policy: shipped(), wantOK: true,
			reason: "the shape a real product uses must be expressible publicly",
		},
		{
			name:   "the zero value is refused",
			policy: new(gate.Policy),
			reason: "no field of it has a safe default",
		},
		{
			name: "a gated recovery command is refused",
			policy: &gate.Policy{
				ExemptExact:      []string{"version"},
				RecoveryPaths:    []string{"license create"},
				OnUpdateRequired: gate.UpdateRefuse,
			},
			reason: "an operator whose entitlement was refused would have no path back",
		},
	}
	for _, tt := range tests {
		//: one row per construction a consumer might attempt.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.policy.Validate()
			//: a usable policy returns a genuine nil.
			if tt.wantOK {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil — %s", err, tt.reason)
				}

				return
			}
			//: every other row must refuse, with the code the facade publishes.
			if err == nil {
				t.Fatalf("Validate() = nil, want a refusal — %s", tt.reason)
			}
			if !errs.HasCode(err, gate.CodePolicyInvalid) {
				t.Errorf("errs.HasCode(err, CodePolicyInvalid) = false for %v", err)
			}
		})
	}
}

// TestAConsumerCanActOnEveryOutcome pins that the three outcomes are reachable
// through the public surface and distinguishable once reached.
//
// A facade that exported Decide but not the Outcome constants would compile and
// be unusable: the caller could not name the value it switched on.
func TestAConsumerCanActOnEveryOutcome(t *testing.T) {
	t.Parallel()

	floor := errors.Join(entitlement.ErrUpdateRequired, errors.New("v1.0.0 < v1.2.0"))

	tests := []struct {
		name      string
		action    gate.UpdateAction
		path      []string
		verifyErr error
		want      gate.Outcome
		reason    string
	}{
		{
			name: "allow", action: gate.UpdateRefuse, path: []string{"lint"},
			want:   gate.OutcomeAllow,
			reason: "a clean verification on a gated command",
		},
		{
			name: "refuse", action: gate.UpdateRefuse, path: []string{"lint"},
			verifyErr: entitlement.ErrRevoked, want: gate.OutcomeRefuse,
			reason: "a revocation stops the invocation",
		},
		{
			name: "upgrade", action: gate.UpdateApply, path: []string{"lint"},
			verifyErr: floor, want: gate.OutcomeUpgrade,
			reason: "the floor under UpdateApply asks the caller to upgrade",
		},
		{
			name: "exempt", action: gate.UpdateRefuse, path: []string{"license", "status"},
			verifyErr: entitlement.ErrRevoked, want: gate.OutcomeAllow,
			reason: "the command that repairs a licence runs when the licence is broken",
		},
	}
	for _, tt := range tests {
		//: one row per branch a consumer's switch has to cover.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := shipped()
			policy.OnUpdateRequired = tt.action
			decision := gate.Decide(policy, tt.path, tt.verifyErr)
			//: the value a consumer switches on.
			if decision.Outcome != tt.want {
				t.Fatalf("Decide().Outcome = %v, want %v — %s",
					decision.Outcome, tt.want, tt.reason)
			}
		})
	}
}

// TestTheCauseSurvivesTheFacade pins the thing a summarising gate would break:
// a consumer must be able to tell "cannot decide" from "decided no", through
// the decision, with the spellings it already uses.
func TestTheCauseSurvivesTheFacade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		verifyErr error
		sentinel  error
		code      errs.Code
		reason    string
	}{
		{
			name: "an outage", verifyErr: entitlement.ErrRosterUnreachable,
			sentinel: entitlement.ErrRosterUnreachable, code: entitlement.CodeRosterUnreachable,
			reason: "treating this as a revocation turns an outage into a lockout",
		},
		{
			name: "a revocation", verifyErr: entitlement.ErrRevoked,
			sentinel: entitlement.ErrRevoked, code: entitlement.CodeRevoked,
			reason: "and treating a revocation as an outage would keep serving",
		},
	}
	for _, tt := range tests {
		//: one row per side of the distinction that must never blur.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision := gate.Decide(shipped(), []string{"lint"}, tt.verifyErr)
			//: the spelling most callers reach for first.
			if !errors.Is(decision.Cause, tt.sentinel) {
				t.Errorf("errors.Is(Decide().Cause, %v) = false — %s", tt.sentinel, tt.reason)
			}
			//: and the SDK's own, which must agree.
			if !errs.HasCode(decision.Cause, tt.code) {
				t.Errorf("errs.HasCode(Decide().Cause, %v) = false — %s", tt.code, tt.reason)
			}
		})
	}
}
