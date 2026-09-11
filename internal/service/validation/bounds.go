// Package validation — the ordered-value bounds constraints.
package validation

import (
	"cmp"
	"fmt"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// AtLeast refuses a value below lo. The bound is inclusive: lo itself passes.
// It is the programmatic spelling of the `min=` struct tag; the name avoids
// shadowing Go's `min` builtin at every call site that dot-imports nothing.
//
// It cannot be misconfigured — every value of an ordered type is a legitimate
// floor — so it returns a Constraint directly.
func AtLeast[T cmp.Ordered](lo T) corevalidation.Constraint[T] {
	//: the message is built once, at construction, and echoes only the BOUND —
	//: which is the caller's own literal, never the value under test.
	message := fmt.Sprintf("must be at least %v", lo)
	//: a single comparison per validation.
	return func(path string, value T) corevalidation.ReportValue {
		//: inclusive lower bound.
		if value >= lo {
			//: accepted.
			return nil
		}
		//: below the floor.
		return one(path, ruleMin, message, CodeOutOfRange)
	}
}

// AtMost refuses a value above hi. The bound is inclusive: hi itself passes.
// It is the programmatic spelling of the `max=` struct tag.
//
// It cannot be misconfigured, for the same reason as [AtLeast].
func AtMost[T cmp.Ordered](hi T) corevalidation.Constraint[T] {
	//: same construction-time message, same value-free rule.
	message := fmt.Sprintf("must be at most %v", hi)
	//: a single comparison per validation.
	return func(path string, value T) corevalidation.ReportValue {
		//: inclusive upper bound.
		if value <= hi {
			//: accepted.
			return nil
		}
		//: above the ceiling.
		return one(path, ruleMax, message, CodeOutOfRange)
	}
}

// Between refuses a value outside the closed interval [lo, hi].
//
// It REFUSES lo > hi at construction rather than validating in silence. An
// inverted interval is satisfied by nothing, so the alternative is a validator
// that rejects every value it is ever shown while reporting a plausible
// per-field message — the ADR 0031 failure mode, in its rejecting form. The
// caller who wrote Between(10, 1) meant Between(1, 10), and finds out now
// rather than from a support ticket.
//
// lo == hi is NOT a misconfiguration: an interval of exactly one value is a
// coherent requirement, and refusing it would be the SDK second-guessing a
// legitimate intent.
func Between[T cmp.Ordered](lo, hi T) (constraint corevalidation.Constraint[T], err error) {
	//: an inverted interval can never be satisfied — refuse it here.
	if lo > hi {
		//: name the clause so the fix is obvious from the error alone.
		return nil, rejectConstraint(ruleBetween, "the minimum is greater than the maximum")
	}
	//: message built once; both bounds are the caller's own literals.
	message := fmt.Sprintf("must be between %v and %v", lo, hi)
	//: two comparisons per validation.
	return func(path string, value T) corevalidation.ReportValue {
		//: closed interval on both ends.
		if value >= lo && value <= hi {
			//: accepted.
			return nil
		}
		//: outside the interval; the message does not say which end, because
		//: telling the caller "too large" for a value they must not see back
		//: adds nothing they cannot infer from the bounds.
		return one(path, ruleBetween, message, CodeOutOfRange)
	}, nil
}
