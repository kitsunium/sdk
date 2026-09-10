// Package authz — hosts the attribute-based evaluator.
package authz

import (
	"context"
	"slices"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
)

// RuleValue is one attribute rule: which requests it is about, what it decides
// when it fires, and the condition under which it fires.
type RuleValue struct {
	// Name identifies the rule in diagnostics. It is required, it is written
	// into the refusal's PRIVATE side and its fields, and it never reaches the
	// wire — a public message naming the rule that refused tells the caller
	// how many rules there are and which one to work around.
	Name string
	// Action and Resource select the requests this rule is about, by byte
	// equality. A rule that does not match is not evaluated at all, so its
	// condition never runs on a request it was not written for.
	Action string
	// Resource is the KIND of thing, matching [PermissionValue.Resource].
	Resource string
	// Effect is what the rule decides when [RuleValue.When] holds:
	// [coreauthz.Allow] or [coreauthz.Deny]. [coreauthz.Abstain] — which is
	// the zero value, so an unset Effect lands here — is refused at
	// construction, because a rule that abstains when it fires is a rule that
	// does nothing.
	Effect coreauthz.Decision
	// When is the predicate. It is required: a rule with no condition fires on
	// every matching request, and the caller who wants that writes the
	// always-true predicate themselves so it shows up in the diff.
	When coreauthz.Condition
}

// NewABAC builds an attribute-based [coreauthz.Policy] over a fixed rule set.
//
// Rules whose Action and Resource do not match the request are skipped. Among
// those that match, the rule set is folded by the SAME algorithm the policy
// set is folded by — deny-overrides — so nesting an ABAC policy inside a
// [DenyOverrides] composition changes nothing about the answer.
//
// # Unevaluable is absorbing, whatever the rule's effect
//
// If a matching rule's condition returns an error, the policy returns
// [coreauthz.Deny] carrying that error, immediately. It does not matter
// whether the rule's effect was Allow or Deny: the rule did not run, so
// nothing it would have decided is known, and the only safe reading of an
// unknown is a refusal.
//
// This is what closes the "absent attribute" hole end to end. A condition on
// `department` cannot silently evaluate to false on a subject that has no
// department, so an Allow rule cannot fail open into an abstention that some
// other rule then grants, and a Deny rule cannot fail open into never firing.
//
// # Order affects the diagnostic, never the answer
//
// Rules are visited in declaration order and the first absorbing event — an
// error or a firing Deny — returns. Both produce [coreauthz.Deny], so the
// DECISION is independent of the order; only which of two simultaneous faults
// is reported to the operator depends on it.
func NewABAC(rules ...RuleValue) (policy coreauthz.Policy, err error) {
	//: refuse a rule set that could never decide before building anything.
	if err := validateRules(rules); err != nil {
		//: a construction fault is permanent; the caller fixes code, not input.
		return nil, err
	}
	//: clone so a later append by the caller cannot rewrite a live policy.
	members := slices.Clone(rules)
	//: the returned closure IS the Policy; it reads the rule set and nothing else.
	return func(_ context.Context, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
		//: evaluation needs no context: a condition is pure and does no I/O.
		return evaluateRules(members, request)
	}, nil
}

// evaluateRules folds the matching rules under deny-overrides.
func evaluateRules(rules []RuleValue, request coreauthz.RequestValue) (decision coreauthz.Decision, err error) {
	//: no rule has permitted yet; the identity of the fold is Abstain.
	granted := false
	//: declaration order decides which of two faults is REPORTED, never which
	//: decision is reached: both absorbing events produce Deny.
	for _, rule := range rules {
		//: a rule about another action or resource is not evaluated at all.
		if rule.Action != request.Action() || rule.Resource != request.Resource() {
			//: skip without touching its condition.
			continue
		}
		//: one condition evaluation per matching rule.
		holds, err := rule.When(request)
		//: unevaluable is absorbing regardless of the rule's effect.
		if err != nil {
			//: refuse and carry the cause; the decision beside it is Deny.
			return coreauthz.Deny, err
		}
		//: a condition that does not hold leaves the decision to the others.
		if !holds {
			//: the rule declines; this is NOT a refusal.
			continue
		}
		//: the rule fired — a refusal is absorbing and returns immediately.
		if rule.Effect == coreauthz.Deny {
			//: short-circuit; nothing after it can change the answer.
			return coreauthz.Deny, nil
		}
		//: a grant is recorded and the loop continues, so a later Deny wins.
		granted = true
	}
	//: at least one grant and no refusal permits; otherwise nobody had an opinion.
	return foldResult(granted), nil
}
