package gate

import (
	coregate "github.com/kitsunium/sdk/framework/internal/core/gate"
	svcgate "github.com/kitsunium/sdk/framework/internal/service/gate"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
// It takes policy, your gate policy — a nil one refuses everything, including a
// verification that would have passed; path, the command path relative to the
// root with the root itself NOT included, where nil is the bare root
// invocation; and verify, your entitlement verification, CALLED AT MOST ONCE
// and only when the invocation is not exempt, with a nil verify refusing.
//
// It returns what to do, and everything the verifier said.
func Decide(policy *Policy, path []string, verify func() error) Decision {
	//: delegate verbatim to the service implementation.
	return svcgate.Decide(policy, path, verify)
}
