// Package validation — the presence constraint.
package validation

import corevalidation "github.com/kitsunium/sdk/internal/core/validation"

// requiredMessage is the fixed, value-free explanation of a presence failure.
const requiredMessage string = "is required"

// Required refuses the zero value of T.
//
// # What "absent" means here, and what it cannot mean
//
// A Go value type has no representation for "not supplied": the zero value is
// what a decoder leaves behind for a field the input never mentioned AND for a
// field the input explicitly set to 0, "" or false. Required therefore refuses
// BOTH, and it cannot tell them apart — no constraint operating on T can.
//
// This is stated rather than hidden because it changes designs: a caller who
// must distinguish "the operator did not configure a timeout" from "the
// operator configured a timeout of zero" declares the field as a POINTER, and
// Required on *time.Duration then means exactly "was supplied". The SDK will
// not guess which of the two a bare zero meant.
//
// Required needs no configuration and therefore cannot be misconfigured, which
// is why it returns a Constraint directly rather than a (Constraint, error).
//
// It is constrained to comparable T because presence is "differs from the zero
// value", and a slice is not comparable. Presence of a slice is its element
// count: use Count(1, Unbounded).
func Required[T comparable]() corevalidation.Constraint[T] {
	//: the zero value is computed once, at construction, not per validation.
	var zero T
	//: the check itself is a single comparison — no reflection, no allocation
	//: on the accepting path.
	return func(path string, value T) corevalidation.ReportValue {
		//: anything that differs from the zero value counts as supplied.
		if value != zero {
			//: nil report — the accepting path allocates nothing.
			return nil
		}
		//: absent.
		return one(path, ruleRequired, requiredMessage, CodeRequired)
	}
}
