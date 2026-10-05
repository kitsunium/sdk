package authz

import (
	"context"
	"slices"

	coreauthz "github.com/kitsunium/sdk/internal/core/security/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// denyOverrides is DenyOverrides's body: decl_gen.go writes DenyOverrides, from the
// design, as one call of it.
func denyOverrides(policies ...coreauthz.Policy) coreauthz.Policy {
	//: clone so a later append by the caller cannot rewrite a live policy.
	members := slices.Clone(policies)
	//: the returned closure is the Policy; it holds no mutable state.
	return func(ctx context.Context, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
		//: evaluate all members; only a refusal is allowed to cut this short.
		return evaluateAll(ctx, members, request)
	}
}

// evaluateAll runs every member and folds the results under deny-overrides.
// Split out of the closure so the loop stays inside the function-length budget
// and can be read as one case analysis.
func evaluateAll(ctx context.Context, members []coreauthz.Policy, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
	//: no member has permitted yet; the identity of the fold is Abstain.
	granted := false
	//: every member is visited unless one refuses; that is what makes this
	//: deny-overrides rather than first-applicable.
	for index, member := range members {
		//: a nil member is a misconfiguration, and it refuses.
		if member == nil {
			//: name the position so the wiring bug is findable.
			return coreauthz.Deny, misconfigured(index, "nil policy")
		}
		//: one evaluation per member, results read together.
		memberDecision, memberErr := member(ctx, request)
		//: unevaluable is absorbing — it outranks any grant already recorded.
		if memberErr != nil {
			//: refuse alongside the cause, never Abstain.
			return coreauthz.Deny, memberErr
		}
		//: an out-of-contract decision is a defect, never a verdict.
		if !memberDecision.Valid() {
			//: name the position and the value that was not a decision.
			return coreauthz.Deny, misconfigured(index, memberDecision.String())
		}
		//: refusal is absorbing; nothing after it can change the answer.
		if memberDecision == coreauthz.Deny {
			//: short-circuit — the ONLY short circuit in this fold.
			return coreauthz.Deny, nil
		}
		//: a grant is recorded and the loop CONTINUES; short-circuiting here
		//: would silently turn this into first-applicable.
		granted = granted || memberDecision.Granted()
	}
	//: at least one grant and no refusal permits; otherwise nobody had an opinion.
	return foldResult(granted), nil
}

// foldResult maps the accumulated grant flag onto the final decision.
func foldResult(granted bool) coreauthz.Decision {
	//: a recorded grant survived every member without meeting a refusal.
	if granted {
		//: permitted.
		return coreauthz.Allow
	}
	//: nobody had an opinion — the closure refuses this, it does not grant it.
	return coreauthz.Abstain
}

// misconfigured builds the PolicyMisconfigured refusal for a member that could
// not be evaluated as assembled, naming its position in the composition.
func misconfigured(index int, detail string) error {
	//: origin-wins keeps the sentinel's identity; the position is diagnostic.
	return errs.Wrap(coreauthz.PolicyMisconfigured, errs.WrapParams{},
		errs.Int("member_index", index),
		errs.String("member_detail", detail))
}
