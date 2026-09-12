// Package gate — the classifier behind pkg/v1/gate.
//
// One exported function, and it performs no effect: it reads a policy, a
// command path and whatever the caller's verifier returned, and says what the
// caller should do. The network access, the upgrade and the process exit all
// stay in the caller's own control flow, where they can be refused, logged or
// tested.
package gate

import (
	"errors"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
	coregate "github.com/kitsunium/sdk/internal/core/gate"
)

// Decide classifies one invocation.
//
// The ORDER is the contract, and it is the part worth reading twice.
//
// Exemption is checked FIRST, before verifyErr is even looked at. A caller runs
// the verifier for its own reasons — a daemon may already hold a grant — but an
// exempt command must run whatever the verifier said, or `license status` stops
// working on exactly the machine an operator is trying to diagnose.
//
// The floor is checked BEFORE the refusal is propagated, because an
// out-of-date binary must be told to upgrade whether or not its entitlement is
// also in order: the upgrade is the same action either way, and reporting
// "your licence is broken" to somebody whose answer is "run upgrade" sends them
// to the wrong place.
//
// Parameters:
//   - policy: the product's gate policy. A nil policy exempts nothing, which
//     is the refusing direction.
//   - path: the command path relative to the root, root NOT included. Nil is
//     the bare root invocation.
//   - verifyErr: what the caller's entitlement verification returned. Nil
//     means it passed.
//
// Returns:
//   - decision: what the caller should do, and everything the verifier said.
func Decide(policy *coregate.PolicyValue, path []string, verifyErr error) coregate.DecisionValue {
	//: Exempt first, and without reading verifyErr: an exempt command runs
	//: whatever the verifier said, which is the whole point of exempting it.
	if policy.Exempt(path) {
		//: Allowed, and recorded as never checked rather than as checked and
		//: allowed — a caller must not seed a watchdog from an exemption.
		return coregate.DecisionValue{Outcome: coregate.OutcomeAllow, Exempt: true}
	}
	//: A verification that passed and a floor that is met is the ordinary path.
	if verifyErr == nil {
		//: Entitled.
		return coregate.DecisionValue{Outcome: coregate.OutcomeAllow}
	}
	//: The floor is a different condition from a refused entitlement even
	//: though it arrives as an error: the subject IS entitled, the binary is
	//: simply too old, and the action is the same whatever the licence says.
	if errors.Is(verifyErr, coreent.ErrUpdateRequired) {
		//: Let the policy decide what an out-of-date build does.
		return floorDecision(policy, verifyErr)
	}

	//: Anything else is a refusal, carried whole so errors.Is and errs.CodeOf
	//: answer through it exactly as they would on the verifier's own error.
	return coregate.DecisionValue{Outcome: coregate.OutcomeRefuse, Cause: verifyErr}
}

// floorDecision applies the policy's UpdateAction to a build below the floor.
//
// Parameters:
//   - policy: the product's gate policy.
//   - cause: the floor refusal, which names the required version.
//
// Returns:
//   - decision: what the caller should do, with FloorUnmet set in every branch.
func floorDecision(policy *coregate.PolicyValue, cause error) coregate.DecisionValue {
	decision := coregate.DecisionValue{Cause: cause, FloorUnmet: true}
	//: A nil policy has no action to read, and refusing is the direction an
	//: absent decision must take.
	if policy == nil {
		//: Refuse.
		decision.Outcome = coregate.OutcomeRefuse

		return decision
	}
	//: The three answers, and the unset value that is none of them.
	switch policy.OnUpdateRequired {
	case coregate.UpdateApply:
		//: The caller upgrades and re-runs; Cause names the required version.
		decision.Outcome = coregate.OutcomeUpgrade
	case coregate.UpdateWarn:
		//: Run anyway. FloorUnmet is what the caller reports alongside.
		decision.Outcome = coregate.OutcomeAllow
	case coregate.UpdateRefuse:
		//: Stop, and report the floor.
		decision.Outcome = coregate.OutcomeRefuse
	default:
		//: An unset action reaches here only from a policy that never went
		//: through Validate. Refuse rather than guess — the same direction
		//: Validate would have taken at start-up.
		decision.Outcome = coregate.OutcomeRefuse
	}

	//: The decision, with the floor recorded whichever branch set the outcome.
	return decision
}
