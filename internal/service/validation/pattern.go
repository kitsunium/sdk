// Package validation — the regular-expression constraint.
package validation

import (
	"regexp"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// Matches refuses a string that the pattern does not match anywhere. Anchor it
// with ^ and $ when the whole string must match — that is regexp's own
// convention and inventing a different one here would surprise everyone who
// already knows it.
//
// The pattern is COMPILED AT CONSTRUCTION and an uncompilable one is refused
// (ADR 0031). A pattern compiled lazily on first use would turn a typo in a
// rarely-exercised rule into a failure at the worst possible moment; compiling
// now means a bad pattern cannot reach production at all.
//
// # Why this is safe against the input
//
// Go's regexp is RE2: no backtracking, linear time in the length of the input.
// A pattern cannot be turned into a denial of service by the value it is shown,
// which is what makes it acceptable to run one on untrusted input at all. It
// also means the SDK does NOT need — and does not impose — an input length cap
// here; cap the length with [Length] if the field has one, for its own reasons.
//
// Matches is deliberately absent from the struct-tag dialect. See [Struct].
func Matches(pattern string) (constraint corevalidation.Constraint[string], err error) {
	//: compile now: a bad pattern is a source defect, not a runtime condition.
	compiled, compileErr := regexp.Compile(pattern)
	//: refuse rather than carry a nil *Regexp that would panic on first use.
	if compileErr != nil {
		//: the clause quotes regexp's own diagnosis, which names the position.
		return nil, rejectConstraint(rulePattern, compileErr.Error())
	}
	//: the message echoes the PATTERN — the caller's own literal — and never
	//: the value, which is the whole point of the value-free message rule.
	message := "must match the pattern " + clip(pattern)
	//: one RE2 scan per validation.
	return func(path, value string) corevalidation.ReportValue {
		//: MatchString is unanchored by design; see the doc comment.
		if compiled.MatchString(value) {
			//: accepted.
			return nil
		}
		//: no match.
		return one(path, rulePattern, message, CodePatternMismatch)
	}, nil
}
