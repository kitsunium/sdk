// Package gate — the classifier's own suite.
//
// What it pins is the ORDER, because the order is the contract and every one of
// these cases is a way of getting it wrong that compiles.
package gate

import (
	"errors"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	coregate "github.com/kitsunium/sdk/internal/core/gate"
)

// errRevoked stands in for any refusal that is not the version floor.
var errRevoked = coreent.ErrRevoked

// errFloor is the floor refusal, wrapped the way the service layer wraps it so
// the test exercises errors.Is rather than a bare sentinel comparison.
var errFloor = errors.Join(coreent.ErrUpdateRequired, errors.New("build is v1.0.0, minimum is v1.2.0"))

// policyWith returns a usable policy carrying the given update action.
func policyWith(action coregate.UpdateAction) *coregate.PolicyValue {
	return &coregate.PolicyValue{
		ExemptExact:      []string{"", "version", "upgrade"},
		ExemptSubtree:    []string{"license"},
		RecoveryPaths:    []string{"upgrade", "license create"},
		OnUpdateRequired: action,
	}
}

// TestDecide pins the order, which is where every interesting mistake lives.
func TestDecide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		policy      *coregate.PolicyValue
		path        []string
		verifyErr   error
		want        coregate.Outcome
		wantExempt  bool
		wantNoCalls bool
		wantFloor   bool
		wantCause   bool
		reason      string
	}{
		{
			name:   "a gated command with a clean verification runs",
			policy: policyWith(coregate.UpdateRefuse), path: []string{"lint"},
			want:   coregate.OutcomeAllow,
			reason: "the ordinary path",
		},
		{
			//: THE ordering case. An exempt command runs whatever the verifier
			//: said, or `license status` stops working on exactly the machine
			//: an operator is trying to diagnose.
			name:   "an exempt command runs even when verification failed",
			policy: policyWith(coregate.UpdateRefuse), path: []string{"license", "status"},
			verifyErr: errRevoked,
			want:      coregate.OutcomeAllow, wantExempt: true,
			reason: "exemption is read BEFORE verifyErr, not after",
		},
		{
			//: And it must not carry a Cause: an exempt invocation was never
			//: checked, so there is nothing for the caller to report.
			name:   "an exempt command reports no cause",
			policy: policyWith(coregate.UpdateRefuse), path: []string{"version"},
			verifyErr: errRevoked,
			want:      coregate.OutcomeAllow, wantExempt: true, wantCause: false,
			reason: "never checked is not the same as checked and allowed",
		},
		{
			name:   "an ordinary refusal stops the invocation",
			policy: policyWith(coregate.UpdateRefuse), path: []string{"lint"},
			verifyErr: errRevoked,
			want:      coregate.OutcomeRefuse, wantCause: true,
			reason: "the cause is carried whole so errors.Is answers through it",
		},
		{
			//: The floor is a different condition even though it arrives as an
			//: error: the subject IS entitled, the binary is simply too old.
			name:   "the floor under UpdateRefuse stops and reports it",
			policy: policyWith(coregate.UpdateRefuse), path: []string{"lint"},
			verifyErr: errFloor,
			want:      coregate.OutcomeRefuse, wantFloor: true, wantCause: true,
			reason: "a conservative product refuses and names the required version",
		},
		{
			name:   "the floor under UpdateApply asks for an upgrade",
			policy: policyWith(coregate.UpdateApply), path: []string{"lint"},
			verifyErr: errFloor,
			want:      coregate.OutcomeUpgrade, wantFloor: true, wantCause: true,
			reason: "the Cause is what names the version, so a caller must still read it",
		},
		{
			//: The only action that lets an out-of-date build keep working,
			//: and the reason FloorUnmet is separate from Outcome.
			name:   "the floor under UpdateWarn runs anyway and still says so",
			policy: policyWith(coregate.UpdateWarn), path: []string{"lint"},
			verifyErr: errFloor,
			want:      coregate.OutcomeAllow, wantFloor: true, wantCause: true,
			reason: "reading Outcome alone would lose the fact the caller has to report",
		},
		{
			//: A policy that never went through Validate. Refuse rather than
			//: guess — the direction Validate would have taken at start-up.
			name:   "the floor under an unset action refuses",
			policy: &coregate.PolicyValue{ExemptExact: []string{"version"}}, path: []string{"lint"},
			verifyErr: errFloor,
			want:      coregate.OutcomeRefuse, wantFloor: true, wantCause: true,
			reason: "an unset action must not silently become one of the three",
		},
		{
			//: A gate nobody configured refuses everything, and does not pay
			//: for a verification that cannot change the answer. The policy is
			//: what says which commands may run at all.
			name:   "a nil policy refuses without verifying",
			policy: nil, path: []string{"version"},
			verifyErr:   nil,
			want:        coregate.OutcomeRefuse,
			wantNoCalls: true,
			reason:      "a gate nobody configured must not allow anything, not even a clean verification",
		},
		{
			//: THE case review caught: a nil policy plus a verification that
			//: PASSED used to fall through and allow, contradicting the
			//: fail-closed contract this package documents.
			name:   "a nil policy refuses a clean verification too",
			policy: nil, path: []string{"lint"},
			verifyErr:   nil,
			want:        coregate.OutcomeRefuse,
			wantNoCalls: true,
			reason:      "the documentation said refuse and the code allowed",
		},
		{
			name:   "the bare root is exempt",
			policy: policyWith(coregate.UpdateRefuse), path: nil,
			verifyErr: errRevoked,
			want:      coregate.OutcomeAllow, wantExempt: true,
			reason: "showing help must not require an entitlement",
		},
	}
	for _, tt := range tests {
		//: one row per ordering the classifier must get right.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			got := Decide(tt.policy, tt.path, func() error {
				calls++

				return tt.verifyErr
			})
			//: An exempt invocation must not pay for a verification it is
			//: exempt from — `completion` runs from a shell hook where a
			//: network round trip would be hostile. Counting is the only
			//: way to assert something did NOT happen.
			if (tt.wantExempt || tt.wantNoCalls) && calls != 0 {
				t.Errorf("Decide() called the verifier %d time(s) when it should not "+
					"have, want 0 — %s", calls, tt.reason)
			}
			//: And a gated one must pay exactly once: twice is two round
			//: trips where the caller budgeted for one.
			if !tt.wantExempt && !tt.wantNoCalls && calls != 1 {
				t.Errorf("Decide() called the verifier %d time(s) on a gated "+
					"invocation, want exactly 1 — %s", calls, tt.reason)
			}
			//: the outcome is what the caller switches on.
			if got.Outcome != tt.want {
				t.Fatalf("Decide().Outcome = %v, want %v — %s", got.Outcome, tt.want, tt.reason)
			}
			//: never checked and checked-and-allowed are the same Outcome and
			//: a very different fact; a daemon seeds a watchdog from one only.
			if got.Exempt != tt.wantExempt {
				t.Errorf("Decide().Exempt = %v, want %v — %s", got.Exempt, tt.wantExempt, tt.reason)
			}
			//: FloorUnmet survives whichever branch set the outcome.
			if got.FloorUnmet != tt.wantFloor {
				t.Errorf("Decide().FloorUnmet = %v, want %v — %s",
					got.FloorUnmet, tt.wantFloor, tt.reason)
			}
			//: the cause is carried, or deliberately absent.
			if (got.Cause != nil) != tt.wantCause {
				t.Errorf("Decide().Cause = %v, want non-nil=%v — %s",
					got.Cause, tt.wantCause, tt.reason)
			}
		})
	}
}

// TestDecideCarriesTheCauseWhole pins that a caller keeps everything the
// verifier said.
//
// A gate that summarised the cause — replacing it with its own sentinel, or
// with a message — would force every consumer to match on text, and the one
// distinction this domain must never blur is "cannot decide" against "decided
// no".
func TestDecideCarriesTheCauseWhole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		verifyErr error
		sentinel  error
		reason    string
	}{
		{
			name: "a revocation stays a revocation", verifyErr: errRevoked,
			sentinel: coreent.ErrRevoked,
			reason:   "the caller maps this to its own exit status",
		},
		{
			name: "an outage stays an outage", verifyErr: coreent.ErrRosterUnreachable,
			sentinel: coreent.ErrRosterUnreachable,
			reason:   "reporting an outage as a revocation is the one wrong answer",
		},
		{
			name: "the floor is still reachable through the decision", verifyErr: errFloor,
			sentinel: coreent.ErrUpdateRequired,
			reason:   "an upgrading caller reads the required version off it",
		},
	}
	for _, tt := range tests {
		//: one row per refusal a caller has to tell apart.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Decide(policyWith(coregate.UpdateRefuse), []string{"lint"},
				func() error { return tt.verifyErr })
			//: errors.Is must answer through Cause exactly as it would on the
			//: verifier's own error.
			if !errors.Is(got.Cause, tt.sentinel) {
				t.Errorf("errors.Is(Decide().Cause, %v) = false — %s", tt.sentinel, tt.reason)
			}
		})
	}
}

// TestDecideRefusesWithoutAVerifier pins that a nil verifier refuses.
//
// It is a programming error rather than a configuration one, and the answer is
// still the refusing direction: nothing vouched for this invocation, so nothing
// may run on the strength of it.
func TestDecideRefusesWithoutAVerifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   []string
		want   coregate.Outcome
		reason string
	}{
		{
			name: "a gated command with no verifier", path: []string{"lint"},
			want:   coregate.OutcomeRefuse,
			reason: "nothing vouched for it",
		},
		{
			name: "an exempt command needs no verifier", path: []string{"version"},
			want:   coregate.OutcomeAllow,
			reason: "exemption is settled before a verifier is ever reached for",
		},
	}
	//: one row per side of the exemption boundary.
	for _, tt := range tests {
		//: each side is its own subtest, so a failure names it.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: a nil function, which is what a caller that forgot one passes.
			if got := Decide(policyWith(coregate.UpdateRefuse), tt.path, nil); got.Outcome != tt.want {
				t.Errorf("Decide(…, nil).Outcome = %v, want %v — %s", got.Outcome, tt.want, tt.reason)
			}
		})
	}
}
