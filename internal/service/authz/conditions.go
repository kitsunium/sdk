// Package authz — hosts the built-in conditions an ABAC rule is written with.
//
// Every one of them reports an ABSENT or WRONG-KIND attribute as an error
// rather than as false. That is the rule the whole domain turns on: a
// comparison against an attribute the request does not carry is not a
// comparison that failed, it is one that never happened, and the two have
// opposite consequences the moment a rule is negated or combined.
//
// There is deliberately no Always condition. An unconditional rule is a grant
// with no reason, and the caller who wants one writes the predicate at the
// call site, where it appears in the diff and in review.
package authz

import (
	coreauthz "github.com/kitsunium/sdk/internal/core/authz"
)

// AttrEquals holds when the request carries key as text equal to want.
//
// It does NOT hold — it FAILS — when the attribute is absent or is not text.
// The canonical example is the one this domain exists for: `department ==
// "finance"` on a subject with no department. Answering false there would let
// a request carrying no attributes at all satisfy every "deny unless" rule of
// that shape by simply omitting the field.
func AttrEquals(key, want string) (condition coreauthz.Condition, err error) {
	//: an unnamed attribute can never be looked up.
	if key == "" {
		//: refuse at construction; the fix is one line at the call site.
		return nil, conditionFault("AttrEquals: key is empty", key)
	}
	//: the returned closure IS the Condition; it reads one attribute.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: absence and kind mismatch both leave as errors, never as false.
		attr, err := lookupAttr(request, key, coreauthz.KindString)
		if err != nil {
			//: unevaluable — the caller's fold treats this as absorbing.
			return false, err
		}
		//: the kind is guaranteed by lookupAttr, so ok is not re-checked.
		got, _ := attr.StringValue()
		//: a plain comparison, on two values that both really exist.
		return got == want, nil
	}, nil
}

// AttrIsTrue holds when the request carries key as a flag that is set.
//
// A flag that is present and false does not hold — that is an answer. A flag
// that is absent fails — that is not. This is the pair map[string]string
// cannot express, and the reason [coreauthz.KindBool] exists.
func AttrIsTrue(key string) (condition coreauthz.Condition, err error) {
	//: an unnamed attribute can never be looked up.
	if key == "" {
		//: refuse at construction.
		return nil, conditionFault("AttrIsTrue: key is empty", key)
	}
	//: the returned closure IS the Condition; it reads one attribute.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: absence and kind mismatch both leave as errors, never as false.
		attr, err := lookupAttr(request, key, coreauthz.KindBool)
		if err != nil {
			//: unevaluable — absorbing in every combinator.
			return false, err
		}
		//: the kind is guaranteed by lookupAttr, so ok is not re-checked.
		got, _ := attr.BoolValue()
		//: present and set is the only holding case.
		return got, nil
	}, nil
}

// AttrAtLeast holds when the request carries key as a number greater than or
// equal to lo. Timestamps travel as Unix seconds, so "the credential was
// issued after T" is this condition.
func AttrAtLeast(key string, lo int64) (condition coreauthz.Condition, err error) {
	//: an unnamed attribute can never be looked up.
	if key == "" {
		//: refuse at construction.
		return nil, conditionFault("AttrAtLeast: key is empty", key)
	}
	//: the returned closure IS the Condition; it reads one attribute.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: absence and kind mismatch both leave as errors, never as false.
		attr, err := lookupAttr(request, key, coreauthz.KindInt64)
		if err != nil {
			//: unevaluable — absorbing in every combinator.
			return false, err
		}
		//: the kind is guaranteed by lookupAttr, so ok is not re-checked.
		got, _ := attr.Int64Value()
		//: a numeric comparison on a value that really is numeric — the whole
		//: point of the typed bag, since "9" > "10" as text.
		return got >= lo, nil
	}, nil
}

// AttrContains holds when the request carries key as a set containing want.
// It is how a scope, a group or a second role dimension is checked from an
// ABAC rule; the role dimension proper belongs to [NewRBAC].
func AttrContains(key, want string) (condition coreauthz.Condition, err error) {
	//: an unnamed attribute can never be looked up.
	if key == "" {
		//: refuse at construction.
		return nil, conditionFault("AttrContains: key is empty", key)
	}
	//: an empty member can never be present in a set a caller meant to write.
	if want == "" {
		//: refuse rather than build a condition that can only not hold.
		return nil, conditionFault("AttrContains: want is empty", key)
	}
	//: the returned closure IS the Condition; it reads one attribute.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: absence and kind mismatch both leave as errors, never as false.
		attr, err := lookupAttr(request, key, coreauthz.KindStrings)
		if err != nil {
			//: unevaluable — absorbing in every combinator.
			return false, err
		}
		//: Contains scans in place; the kind is already guaranteed.
		found, _ := attr.Contains(want)
		//: an empty set does not contain want, and that IS an answer.
		return found, nil
	}, nil
}

// AttrMatchesSubject holds when the request carries key as text equal to the
// request's subject. It is the ownership rule — "the author may edit it" —
// written without the SDK ever learning what ownership means.
//
// # An anonymous request owns nothing
//
// When the subject is empty, the condition does not hold, even against an
// attribute that is also empty. The comparison is answerable here — the
// request really has no subject — so this is a genuine false and not a
// swallowed absence. It is spelled out because empty-matches-empty would make
// every unowned resource owned by every unauthenticated caller.
func AttrMatchesSubject(key string) (condition coreauthz.Condition, err error) {
	//: an unnamed attribute can never be looked up.
	if key == "" {
		//: refuse at construction.
		return nil, conditionFault("AttrMatchesSubject: key is empty", key)
	}
	//: the returned closure IS the Condition; it reads one attribute.
	return func(request coreauthz.RequestValue) (holds bool, err error) {
		//: absence and kind mismatch both leave as errors, never as false.
		attr, err := lookupAttr(request, key, coreauthz.KindString)
		if err != nil {
			//: unevaluable — absorbing in every combinator.
			return false, err
		}
		//: the kind is guaranteed by lookupAttr, so ok is not re-checked.
		owner, _ := attr.StringValue()
		//: an empty subject never matches, not even an empty owner.
		return request.Subject() != "" && owner == request.Subject(), nil
	}, nil
}

// lookupAttr resolves key and asserts its kind, reporting the two failures
// that must never be reported as false.
func lookupAttr(request coreauthz.RequestValue, key string, want coreauthz.AttrKind) (attr coreauthz.AttrValue, err error) {
	//: absence is unanswerable, not false.
	found, ok := request.Attr(key)
	//: the request never carried this attribute — the rule did not run.
	if !ok {
		//: KindInvalid renders as "absent" in the diagnostic field.
		return coreauthz.AttrValue{}, attrFault(coreauthz.AttributeMissing, key, want, coreauthz.KindInvalid)
	}
	//: present but the wrong kind is the same failure wearing another hat.
	if found.Kind() != want {
		//: report both kinds so the producing side can be fixed.
		return coreauthz.AttrValue{}, attrFault(coreauthz.AttributeKindMismatch, key, want, found.Kind())
	}
	//: the attribute exists and is the kind the rule compares.
	return found, nil
}
