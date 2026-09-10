// Package authz — hosts the condition combinators.
//
// All three share one rule: an unevaluable branch is ABSORBING. If any branch
// reports that it could not be evaluated, the combinator reports the same and
// evaluates no answer from the others.
//
// That rule is what stops an absent attribute from being laundered into a
// grant. Negation is the sharpest case — Not over a condition that could not
// be evaluated must not become "true" — but AnyOf is the same defect one step
// further out: returning true because a sibling branch held would mean a
// request that omits an attribute satisfies a rule the attribute was there to
// constrain.
package authz

import (
	"slices"

	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
)

// Not inverts a condition's answer and propagates its failure UNCHANGED.
//
// This is the negation trap, and it is worth stating as its own paragraph. If
// a missing attribute were reported as false, Not would turn it into true —
// so a rule reading "deny unless the department is finance", written as
// Not(AttrEquals("department", "finance")), would FIRE for a subject with no
// department under one reading and NOT fire under the other. Either way, the
// subject that carries the fewest attributes gets the most favourable answer.
//
// Because the inner condition reports absence as an error and Not passes that
// error through instead of flipping it, the subject that carries no attributes
// gets a refusal.
func Not(inner coreauthz.Condition) (condition coreauthz.Condition, err error) {
	//: a nil condition has no answer to invert.
	if inner == nil {
		//: refuse at construction.
		return nil, conditionFault("Not: condition is nil", "")
	}
	//: the returned closure IS the Condition; it wraps exactly one inner.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: one evaluation; the two results are read together.
		innerHolds, innerErr := inner(request)
		//: the failure is propagated, NEVER inverted into a hold.
		if innerErr != nil {
			//: false alongside the error, so a caller reading only the bool
			//: still fails closed.
			return false, innerErr
		}
		//: a real answer inverts.
		return !innerHolds, nil
	}, nil
}

// AllOf holds when every condition holds. It evaluates ALL of them — there is
// no short circuit on the first false — so its answer does not depend on the
// order they were listed in, and a failure anywhere is reported rather than
// hidden behind an earlier false.
//
// An empty set is refused: a conjunction of nothing is vacuously true, which
// on an Allow rule is an unconditional grant written as if it were a condition.
func AllOf(conditions ...coreauthz.Condition) (condition coreauthz.Condition, err error) {
	//: refuse an empty or nil-bearing set before building anything.
	members, err := checkedMembers("AllOf", conditions)
	if err != nil {
		//: a construction fault is permanent.
		return nil, err
	}
	//: the returned closure IS the Condition; it folds with AND.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: fold with the conjunction's identity.
		return foldConditions(members, request, true)
	}, nil
}

// AnyOf holds when at least one condition holds. Like [AllOf] it evaluates
// every branch, so an unevaluable branch is reported even when a sibling
// branch would have held — the alternative lets a request satisfy a rule by
// omitting the attribute one of its branches names.
//
// An empty set is refused: a disjunction of nothing is vacuously false, which
// builds a rule that can never fire.
func AnyOf(conditions ...coreauthz.Condition) (condition coreauthz.Condition, err error) {
	//: refuse an empty or nil-bearing set before building anything.
	members, err := checkedMembers("AnyOf", conditions)
	if err != nil {
		//: a construction fault is permanent.
		return nil, err
	}
	//: the returned closure IS the Condition; it folds with OR.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: fold with the disjunction's identity.
		return foldConditions(members, request, false)
	}, nil
}

// foldConditions evaluates every member and combines the answers. conjunction
// selects the operator AND its identity: true folds with AND, false with OR.
func foldConditions(members []coreauthz.Condition, request coreauthz.RequestValue, conjunction bool) (holds bool, err error) {
	//: the identity of the fold doubles as the operator selector.
	result := conjunction
	//: EVERY branch is evaluated — there is no short circuit — so the answer
	//: does not depend on the order the caller listed them in.
	for _, member := range members {
		//: one evaluation per member; no branch is skipped.
		memberHolds, memberErr := member(request)
		//: unevaluable is absorbing — no partial answer is returned with it.
		if memberErr != nil {
			//: false alongside the error, so a caller reading only the bool
			//: still fails closed.
			return false, memberErr
		}
		//: AND on a conjunction, OR on a disjunction.
		if conjunction {
			//: every member must hold.
			result = result && memberHolds
			continue
		}
		//: at least one member must hold.
		result = result || memberHolds
	}
	//: every branch was evaluated, so the answer is order-independent.
	return result, nil
}

// checkedMembers refuses an empty or nil-bearing condition set and returns a
// defensive copy of a valid one.
func checkedMembers(name string, conditions []coreauthz.Condition) (members []coreauthz.Condition, err error) {
	//: a combinator over nothing is vacuous in one direction or the other.
	if len(conditions) == 0 {
		//: refuse rather than build a rule that always or never fires.
		return nil, conditionFault(name+": no conditions", "")
	}
	//: one pass to reject a nil member before any of them is ever called.
	for _, condition := range conditions {
		//: a nil member would panic on the request path.
		if condition == nil {
			//: refuse at construction, where the wiring is visible.
			return nil, conditionFault(name+": condition is nil", "")
		}
	}
	//: clone so a later append by the caller cannot rewrite a live condition.
	//: every member is callable.
	return slices.Clone(conditions), nil
}
