// Package validation implements the SDK's constraint engine over the
// internal/core/validation port: a small, closed set of built-in constraints,
// the combinators that compose them and descend into nested structures, and a
// struct-tag front end that compiles a cached plan per type (ADR 0046).
//
// Two entry shapes, one contract. The PROGRAMMATIC path is generic and uses no
// reflection at all — Field and Each take an accessor function, so every
// descent is a direct field read the compiler can inline. The TAG path trades
// that for ergonomics and is measured rather than assumed; see BENCH.md.
//
// Both produce a core/validation.Constraint, so they compose with each other:
// a struct-tag validator and a hand-written cross-field rule can be handed to
// All and appear in one report.
//
// Collecting EVERY violation is the default and First is the opt-in. A form
// that reports one error at a time makes the user submit it five times.
package validation

import (
	"slices"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// All runs every constraint and returns everything they found, in composition
// order. It is the default shape of the engine: a caller who wanted one answer
// can take the first violation, but a caller who needed all of them cannot
// recover the ones a short-circuit never computed.
//
// All with no constraint is legitimate and accepts everything — a validator
// without rules is not a broken validator (ADR 0031). What is refused is a
// constraint that could never be satisfied, and that is refused by its own
// constructor, before it can be composed.
func All[T any](constraints ...corevalidation.Constraint[T]) corevalidation.Constraint[T] {
	//: capture the list so a later mutation of the caller's slice cannot
	//: silently change what a compiled validator checks.
	fixed := slices.Clone(constraints)
	//: the composed constraint is itself a Constraint — that is what lets a
	//: tag validator and a hand-written rule live in the same report.
	return func(path string, value T) corevalidation.ReportValue {
		//: nil report: the accepting path allocates nothing.
		var report corevalidation.ReportValue
		//: every constraint runs, whatever the earlier ones found.
		for _, constraint := range fixed {
			//: a nil entry is a caller mistake that must not panic mid-request;
			//: it contributes nothing, exactly like an empty constraint list.
			if constraint == nil {
				//: skip it.
				continue
			}
			//: append keeps composition order, which is what makes a report
			//: diffable between two runs.
			report = append(report, constraint(path, value)...)
		}
		//: everything that was wrong, in the order it was checked.
		return report
	}
}

// First runs the constraints in order and stops at the one that refuses,
// returning only its violations. It is a REAL short-circuit — the constraints
// after the first failure are never evaluated — so it is honest to reach for
// when a later check is expensive or would be meaningless.
//
// It is deliberately a combinator rather than a mode flag on a runner. A flag
// that truncated the report after the fact would claim a saving it did not
// make; a combinator that stops really stops, and the caller can see where.
func First[T any](constraints ...corevalidation.Constraint[T]) corevalidation.Constraint[T] {
	//: same defensive copy as All, for the same reason.
	fixed := slices.Clone(constraints)
	//: short-circuiting composition.
	return func(path string, value T) corevalidation.ReportValue {
		//: run in order until one refuses.
		for _, constraint := range fixed {
			//: a nil entry contributes nothing and does not stop the walk.
			if constraint == nil {
				//: skip it.
				continue
			}
			//: evaluate this one.
			if report := constraint(path, value); !report.OK() {
				//: stop here — the remaining constraints are not evaluated.
				return report
			}
		}
		//: every constraint accepted.
		return nil
	}
}

// Check runs constraints against value at the root path and converts the
// result to the SDK error model. It is the one-line bridge to an error-typed
// contract — principally core/config.Validator's Validate() error, which is
// how a decoded config struct self-checks after config.Load.
//
// It returns a genuine nil interface when nothing was found wrong.
func Check[T any](value T, constraints ...corevalidation.Constraint[T]) error {
	//: compose, run from the root, and convert.
	return All(constraints...)(corevalidation.RootPath, value).Err()
}
