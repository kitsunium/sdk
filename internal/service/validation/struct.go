// Package validation — the struct-tag front end.
package validation

import (
	"reflect"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// tagName is the struct tag the compiler reads.
const tagName string = "validate"

// StructConfig tunes a compiled struct validator.
type StructConfig struct {
	// StopAtFirst compiles a plan that stops at the first violation instead of
	// collecting every one. The zero value collects everything, because a form
	// that reports one error at a time makes the user submit it five times.
	//
	// It is compiled INTO the plan, nested plans included, so a stop-at-first
	// validator really does stop: the later fields are never read and the
	// later rules never run. It is not a filter applied to a full report.
	StopAtFirst bool
}

// Struct compiles the `validate` struct tags of T into a Constraint. The plan
// is compiled once per (type, mode) and cached, so calling Struct again for
// the same T is one map read — which is what makes it usable inside a
// core/config.Validator's Validate method:
//
//	func (c Conf) Validate() error {
//	    rules, err := validation.Struct[Conf](validation.StructConfig{})
//	    if err != nil {
//	        return err
//	    }
//	    return validation.Check(c, rules)
//	}
//
// The result IS a Constraint, so tags and hand-written rules compose into one
// report: All(tagRules, crossFieldRule).
//
// # A type with no validate tag is legitimate
//
// It compiles to an empty plan and accepts everything. "No rules" is a rule
// set, and refusing it would make it impossible to add validation to a type
// incrementally. What is refused is a tag that cannot be honoured — and it is
// refused HERE, at compile time, never at the first request (ADR 0031).
//
// # The dialect is a strict subset of the programmatic API, on purpose
//
// Accepted: required, min, max (numbers), minlen, maxlen (string runes),
// mincount, maxcount (slice/array elements), oneof=a|b|c, dive.
//
// Refused BY NAME, each with a message that says what to do instead: pattern
// and regex (a regular expression cannot live in a comma-separated tag without
// inventing an escape dialect, and a SILENTLY TRUNCATED pattern is the exact
// class of failure this SDK refuses — compose Matches in code); email, url and
// uuid (see ADR 0046 §What is refused); dive on a map; any unknown rule.
func Struct[T any](cfg StructConfig) (constraint corevalidation.Constraint[T], err error) {
	//: the tag engine walks fields; anything without fields has nothing to walk.
	typ := reflect.TypeFor[T]()
	//: refuse a non-struct target by naming the kind that was seen.
	if typ.Kind() != reflect.Struct {
		//: no plan is built and nothing is cached.
		return nil, rejectTarget(typ.Kind().String())
	}
	//: compile (or fetch) the plan for this type and mode.
	plan, planErr := planFor(typ, cfg.StopAtFirst)
	//: a refused tag stops here, at construction.
	if planErr != nil {
		//: hand the typed refusal back.
		return nil, planErr
	}
	//: the compiled validator: one reflect.ValueOf per validation, then a walk
	//: of pre-resolved field indices — no tag parsing, no name lookups.
	return func(path string, value T) corevalidation.ReportValue {
		//: run the plan from the caller's path.
		return plan.run(path, reflect.ValueOf(value))
	}, nil
}
