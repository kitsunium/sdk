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

// decide is Decide's body: decl_gen.go writes Decide, from the
// design, as one call of it.
func decide(policy *Policy, path []string, verify func() error) Decision {
	//: delegate verbatim to the service implementation.
	return svcgate.Decide(policy, path, verify)
}
