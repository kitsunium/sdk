// Package validation — the compiled struct-tag plan and its per-type cache.
//
// A plan is compiled ONCE per (type, mode) and reused by every validation of
// that type. Parsing a struct tag on every request would put string splitting
// and reflect.StructField lookups on the hot path of every field of every
// request — which is exactly the shape of the "we added validation and the p99
// moved" post-mortem. See BENCH.md for what the cache is worth.
package validation

import (
	"reflect"
	"sync"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// planStep checks one aspect of a struct value — one field's rules, or one
// descent into a nested value — and reports what it found, located under base.
type planStep func(base string, structValue reflect.Value) corevalidation.ReportValue

// fieldCheck is one compiled rule bound to one field's static type. It reads
// the field through the kind-specific accessor chosen at COMPILE time, so a
// validation performs no type dispatch of its own.
type fieldCheck func(path string, fieldValue reflect.Value) corevalidation.ReportValue

// planCache maps a planKey to its *structPlan. sync.Map is the right shape
// here: the entry set is written once per type at start-up and read on every
// validation forever after.
var planCache sync.Map

// structPlan is the compiled rule set of one struct type.
type structPlan struct {
	steps       []planStep
	stopAtFirst bool
}

// run executes the plan against a struct value located at base.
func (p *structPlan) run(base string, structValue reflect.Value) corevalidation.ReportValue {
	//: nil report — a struct that satisfies every rule allocates nothing here.
	var report corevalidation.ReportValue
	//: field order is declaration order, so the report is stable across runs.
	for _, step := range p.steps {
		//: evaluate this field's rules, or this descent.
		found := step(base, structValue)
		//: nothing wrong with this one.
		if len(found) == 0 {
			//: on to the next.
			continue
		}
		//: a stop-at-first plan compiles its steps and its nested plans in the
		//: same mode, so found holds exactly one violation — returning it is
		//: an exact answer, not a truncated one.
		if p.stopAtFirst {
			//: stop here; the remaining steps are never evaluated.
			return found
		}
		//: collect and keep going — the default.
		report = append(report, found...)
	}
	//: everything that was wrong, in declaration order.
	return report
}

// planFor returns the compiled plan for typ, compiling it on first sight.
// A compile FAILURE is deliberately not cached: it is a source defect that
// fails the same way every time, and caching it would only make the second
// error message harder to trace back to its cause.
func planFor(typ reflect.Type, stopAtFirst bool) (plan *structPlan, err error) {
	//: identity of the plan being asked for.
	key := planKey{typ: typ, stopAtFirst: stopAtFirst}
	//: the steady-state path: one map read, no parsing, no compilation.
	if cached, found := planCache.Load(key); found {
		//: the cache only ever holds *structPlan; the comma-ok form keeps a
		//: future writer of a different value type from panicking a request.
		if plan, ok := cached.(*structPlan); ok {
			//: the published plan.
			return plan, nil
		}
	}
	//: first sight of this type — compile it, guarding against a type cycle.
	compiled, compileErr := compileStruct(typ, stopAtFirst, map[reflect.Type]bool{})
	//: a refused tag is refused here, before any value is ever validated.
	if compileErr != nil {
		//: hand the typed refusal back to the caller of Struct.
		return nil, compileErr
	}
	//: LoadOrStore rather than Store: two goroutines may compile the same type
	//: concurrently, and both plans are equivalent — the first one published
	//: wins so every validator of this type shares one plan.
	actual, _ := planCache.LoadOrStore(key, compiled)
	//: comma-ok for the same reason as above; the freshly compiled plan is the
	//: fallback, and it is equivalent to whatever else could be in there.
	if plan, ok := actual.(*structPlan); ok {
		//: the published plan.
		return plan, nil
	}
	//: unreachable while this package owns the cache — and a compiled plan
	//: rather than a nil is the answer that keeps the caller working if it
	//: ever stops owning it.
	return compiled, nil
}
