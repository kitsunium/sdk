// Package authz — hosts DenyOverrides, the one combining algorithm.
package authz

import (
	"context"
	"slices"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DenyOverrides composes policies into one. A refusal from any member wins; an
// [coreauthz.Allow] is returned only when at least one member permitted and no
// member refused; otherwise the composition abstains.
//
// # Why refusal wins, and why that is not configurable
//
// When one rule permits and another refuses, one of them has to lose, and the
// choice is a security property rather than a preference. Under deny-overrides
// the failure mode of a wrong rule set is a request that should have been
// allowed and was not — visible, reported, fixed within the hour. Under
// permit-overrides it is a request that should have been refused and was not
// — invisible, and discovered by whoever exploits it.
//
// XACML's permit-overrides, first-applicable and only-one-applicable are
// therefore REFUSED BY NAME: this package ships one combining algorithm and no
// setting that selects another. A caller who genuinely needs a grant that
// beats a refusal — a break-glass path — writes it as a Go `if` around the
// composition, where the exception is visible at the call site instead of
// hidden in the semantics of a combiner. See ADR 0057 §D2.
//
// # It evaluates every member, and that is what makes it deny-overrides
//
// An [coreauthz.Allow] does NOT short-circuit. A composition that returned on
// the first grant would be first-applicable wearing this function's name, and
// it would produce a different answer depending on the order the policies were
// listed in — the defect being ruled out here. Only a refusal short-circuits,
// because refusal is absorbing: once one member has said Deny, nothing any
// other member can say changes the result.
//
// The composition is therefore commutative and associative, which is why
// nesting is invisible: DenyOverrides(a, DenyOverrides(b, c)) and
// DenyOverrides(a, b, c) agree, and so does any permutation of them.
//
// # The empty and nil cases
//
// DenyOverrides() with no members abstains — the identity of the combiner —
// and [Check] then refuses. An authorizer with no policy grants nothing, which
// is the exact opposite of internal/service/validation, where a validator with
// no constraint passes. Both are the safe direction for their domain, and the
// contrast is the reason ADR 0031 is about SAFE defaults rather than permissive
// or restrictive ones.
//
// A nil member REFUSES rather than being skipped. Skipping it would silently
// shrink the policy set, which is the single most valuable edit an attacker
// could make to a wiring file.
func DenyOverrides(policies ...coreauthz.Policy) coreauthz.Policy {
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
