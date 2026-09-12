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
// Exemption is checked FIRST, and verify is not CALLED at all when it holds.
// That is why this takes a function rather than an error: a caller handed an
// already-computed result has already paid for it, and an exempt command must
// not pay. `completion` runs from a shell hook where a network round trip would
// be hostile, and `license status` must work on exactly the machine whose
// licence is broken — neither can afford a verification, and neither should
// have to remember not to run one.
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
//   - verify: the caller's entitlement verification, CALLED AT MOST ONCE and
//     only when the invocation is not exempt. Nil is treated as a
//     verification that did not happen, which refuses.
//
// Returns:
//   - decision: what the caller should do, and everything the verifier said.
func Decide(policy *coregate.PolicyValue, path []string, verify func() error) coregate.DecisionValue {
	//: Exempt first, and WITHOUT calling verify: an exempt command must not
	//: pay for a verification it is exempt from.
	if policy.Exempt(path) {
		//: Allowed, and recorded as never checked rather than as checked and
		//: allowed — a caller must not seed a watchdog from an exemption.
		return coregate.DecisionValue{Outcome: coregate.OutcomeAllow, Exempt: true}
	}
	//: A nil verifier verified nothing, which is the refusing direction — the
	//: same one an absent policy takes.
	if verify == nil {
		//: Nothing vouched for this invocation.
		return coregate.DecisionValue{Outcome: coregate.OutcomeRefuse}
	}
	verifyErr := verify()
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
