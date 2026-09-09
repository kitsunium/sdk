// Package validation — the descent combinators. These are what make a
// violation LOCATED on a nested structure, and they do it with an accessor
// function rather than reflection: the programmatic path never imports
// reflect, so a hot validator is a chain of direct field reads the compiler
// can inline.
package validation

import corevalidation "github.com/kitsunium/sdk/internal/core/validation"

// ruleField and ruleEach name the descent combinators in a construction
// refusal. They never appear on a ViolationValue — a descent does not fail,
// it relocates.
const (
	ruleField string = "field"
	ruleEach  string = "each"
)

// Field applies constraints to a member of T reached through get, reporting
// every violation at the parent path extended by name.
//
// name is the member's name as the CALLER wants it to appear in a path; the
// SDK does not read it off the type, because the programmatic path never
// touches reflection. [Struct] derives it from the json tag instead.
//
// A nil get is REFUSED at construction. It is not a missing rule — the rules
// are right there — it is a missing way to reach the value they are about, so
// carrying it would produce a validator that runs, reports nothing, and looks
// exactly like a passing one. That is the ADR 0031 trap in its accepting form,
// which is the more dangerous of the two. An empty name is refused for the
// neighbouring reason: a violation whose path silently points at the parent is
// a violation that is not located.
func Field[T, F any](name string, get func(T) F, constraints ...corevalidation.Constraint[F]) (constraint corevalidation.Constraint[T], err error) {
	//: an unnamed member cannot be located in a path.
	if name == "" {
		//: name the clause.
		return nil, rejectConstraint(ruleField, "the member name is empty")
	}
	//: without an accessor the constraints below can never run.
	if get == nil {
		//: name the clause.
		return nil, rejectConstraint(ruleField, "the accessor is nil")
	}
	//: compose the member's rules once — collect-all is the default here too.
	inner := All(constraints...)
	//: descent is a path extension plus a delegated run.
	return func(path string, value T) corevalidation.ReportValue {
		//: the member's path, in the core grammar.
		return inner(corevalidation.JoinField(path, name), get(value))
	}, nil
}

// Each applies constraints to EVERY element of a slice member of T reached
// through get, reporting each element's violations at the parent path extended
// by name and the element's index — "addresses[2]".
//
// Every element is visited, including those after the first failing one: an
// import that reports row 3 and stops makes the operator run the import six
// times. The elements are visited in slice order, so the report is stable
// across runs, which is what makes two reports diffable.
//
// A nil slice contributes nothing — it holds no element to be wrong about.
// Requiring at least one element is [Count], a different question asked at the
// member itself.
//
// A nil get, or an empty name, is refused at construction for the reasons
// [Field] gives.
func Each[T, E any](name string, get func(T) []E, constraints ...corevalidation.Constraint[E]) (constraint corevalidation.Constraint[T], err error) {
	//: an unnamed member cannot be located in a path.
	if name == "" {
		//: name the clause.
		return nil, rejectConstraint(ruleEach, "the member name is empty")
	}
	//: without an accessor the constraints below can never run.
	if get == nil {
		//: name the clause.
		return nil, rejectConstraint(ruleEach, "the accessor is nil")
	}
	//: compose the element rules once, not per element.
	inner := All(constraints...)
	//: element-wise descent.
	return func(path string, value T) corevalidation.ReportValue {
		//: the slice's own path; each element extends it with its index.
		base := corevalidation.JoinField(path, name)
		//: nil report — an accepting slice allocates nothing.
		var report corevalidation.ReportValue
		//: slice order is report order.
		for index, element := range get(value) {
			//: every element is visited, whatever the earlier ones reported.
			report = append(report, inner(corevalidation.JoinIndex(base, index), element)...)
		}
		//: everything that was wrong, in index order.
		return report
	}, nil
}

// Must returns constraint, panicking when err is non-nil. It is the
// regexp.MustCompile / template.Must idiom, for the same situation: a
// constraint built from source literals at package initialisation, where the
// only possible error is a defect the binary must not start with.
//
// Use it for a package-level var. Do NOT use it on a bound that comes from
// configuration — there the error is a real runtime condition and must be
// returned, which is why every fallible constructor hands one back.
func Must[T any](constraint corevalidation.Constraint[T], err error) corevalidation.Constraint[T] {
	//: a construction failure at init must stop the binary, not be carried.
	if err != nil {
		//: the message is the typed error's own rendering — code, reason and
		//: the clause that failed — so an operator can grep the dotted quad.
		panic(err.Error())
	}
	//: well-formed; hand it back unchanged.
	return constraint
}
