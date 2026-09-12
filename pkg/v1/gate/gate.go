//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/gate .

// Package gate decides whether one invocation of a distributed binary may run.
//
// A product that verifies an entitlement at start-up answers three questions
// before it does any work, and they are usually answered by hand, in a root
// command, in code nobody revisits:
//
//   - Is THIS invocation subject to the check at all?
//   - The vendor mandates a newer build. Refuse, upgrade, or warn?
//   - When the answer is no, what should the process do about it?
//
// This package owns the first two as values and leaves the third to you.
//
// # It performs nothing
//
// [Decide] does not verify — that is pkg/v1/entitlement. It does not upgrade —
// that is pkg/v1/selfupdate. And it never ends your process. It reads what your
// verifier returned and says what to do; the effects stay in your control flow,
// where they can be refused, logged or tested.
//
//	decision := gate.Decide(policy, invocation.Path[1:], func() error {
//		_, err := service.Verify(time.Now())
//		return err
//	})
//	switch decision.Outcome {
//	case gate.OutcomeAllow:
//		// run the command; decision.FloorUnmet may still want reporting
//	case gate.OutcomeUpgrade:
//		// upgrade AT MOST ONCE, then re-run
//	default:
//		fmt.Fprintln(os.Stderr, decision.Cause)
//		os.Exit(errs.ExitCodeOf(decision.Cause))
//	}
//
// # The policy refuses to be unsafe
//
// [Policy.Validate] reports every fault at once rather than the first, and two
// of them are lockouts rather than typos: a policy that exempts nothing cannot
// be repaired from inside the binary, and a policy that gates its own recovery
// command leaves a machine whose entitlement lapsed with no path back short of
// reinstalling. [Policy.RecoveryPaths] is what turns the second from a comment
// into a construction-time refusal.
package gate

import (
	coregate "github.com/kitsunium/sdk/internal/core/gate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcgate "github.com/kitsunium/sdk/internal/service/gate"
)

// CodePolicyInvalid identifies a gate policy that could not be used as written.
const CodePolicyInvalid errs.Code = coregate.CodePolicyInvalid

// UpdateRefuse stops the invocation and reports the floor.
const UpdateRefuse UpdateAction = coregate.UpdateRefuse

// UpdateApply asks the caller to upgrade and re-run the invocation.
const UpdateApply UpdateAction = coregate.UpdateApply

// UpdateWarn runs the invocation anyway and reports the floor alongside it.
const UpdateWarn UpdateAction = coregate.UpdateWarn

// OutcomeAllow runs the invocation.
const OutcomeAllow Outcome = coregate.OutcomeAllow

// OutcomeRefuse stops the invocation; Decision.Cause says why.
const OutcomeRefuse Outcome = coregate.OutcomeRefuse

// OutcomeUpgrade asks the caller to upgrade and re-run.
const OutcomeUpgrade Outcome = coregate.OutcomeUpgrade

// Policy is one product's gate policy: what runs unchecked, what must never be
// gated, and what a mandated upgrade does. Its zero value is refused by
// Validate.
type Policy = coregate.PolicyValue

// Decision is what the gate concluded about one invocation. It carries no
// method that acts.
type Decision = coregate.DecisionValue

// UpdateAction is what happens when the vendor mandates a newer build. Its zero
// value is unclaimed, because refusing and upgrading are opposite answers.
type UpdateAction = coregate.UpdateAction

// Outcome is what the caller should do with an invocation. Its zero value is
// unclaimed, so a decision nobody made never reads as "allow".
type Outcome = coregate.Outcome

// Decide classifies one invocation.
//
// Exemption is checked FIRST, and verify is not CALLED when it holds — which
// is why this takes a function rather than an error. A shell-completion hook or
// a `license status` on a machine whose licence is broken must not pay for a
// network round trip, and neither should have to remember not to run one.
//
// The version floor is checked BEFORE a refusal is propagated, because an
// out-of-date binary must be told to upgrade whether or not its entitlement is
// also in order.
//
// Parameters:
//   - policy: the product's gate policy. A nil policy exempts nothing.
//   - path: the command path relative to the root, root NOT included. Nil is
//     the bare root invocation.
//   - verify: your entitlement verification, CALLED AT MOST ONCE and only
//     when the invocation is not exempt. Nil refuses.
//
// Returns:
//   - decision: what to do, and everything the verifier said.
func Decide(policy *Policy, path []string, verify func() error) Decision {
	//: delegate verbatim to the service implementation.
	return svcgate.Decide(policy, path, verify)
}
