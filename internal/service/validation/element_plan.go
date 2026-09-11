// Package validation — hosts elementPlan, the rule set one element of a dived
// slice or array is checked against.
package validation

import (
	"reflect"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// elementPlan is the compiled rule set applied to each element of a slice or
// array: the element's own rules, and — when the element is a struct — its
// nested plan. It carries the mode too, so a stop-at-first descent stops
// inside an element rather than only between elements.
//
// presence holds the presence rule of a POINTER element, kept apart from
// checks because it asks about a different value: the pointer, where every
// other rule asks about what it points at. It is empty for any other element,
// whose presence rule sits in checks in tag order.
type elementPlan struct {
	presence    []fieldCheck
	checks      []fieldCheck
	nested      *structPlan
	deref       bool
	stopAtFirst bool
}

// check runs one element's rules and its nested plan.
func (p elementPlan) check(path string, element reflect.Value) corevalidation.ReportValue {
	//: presence runs first, on the element as stored: a nil *Element is the
	//: very absence it asks about, so it must be asked before the pointer is
	//: followed or skipped.
	report, stopped := p.apply(nil, p.presence, path, element)
	//: a stop-at-first plan has its one violation.
	if stopped {
		//: exact, not truncated.
		return report
	}
	//: a pointer element has a value behind it, or nothing at all.
	if p.deref {
		//: a nil *Element holds nothing else to be wrong about.
		if element.IsNil() {
			//: whatever presence said is the whole answer.
			return report
		}
		//: follow the pointer.
		element = element.Elem()
	}
	//: the element's own rules.
	report, stopped = p.apply(report, p.checks, path, element)
	//: a stop-at-first plan has its one violation.
	if stopped {
		//: the nested plan never runs.
		return report
	}
	//: then its nested structure, if it has one.
	if p.nested != nil {
		//: located under the element's own path; the nested plan carries the
		//: same mode, so it too reports exactly one violation when stopping.
		report = append(report, p.nested.run(path, element)...)
	}
	//: everything this element got wrong.
	return report
}

// apply runs checks against value at path, appending what they report to
// sofar. stopped reports that a stop-at-first plan found its violation, in
// which case report is exactly that one violation: nothing was found before
// it, or the plan would already have stopped.
func (p elementPlan) apply(sofar corevalidation.ReportValue, checks []fieldCheck, path string, value reflect.Value) (report corevalidation.ReportValue, stopped bool) {
	//: extend what the element has already reported.
	report = sofar
	//: rules run in tag order.
	for _, check := range checks {
		//: evaluate.
		found := check(path, value)
		//: accepted.
		if len(found) == 0 {
			//: next rule.
			continue
		}
		//: a single check reports at most one violation, so this is exact.
		if p.stopAtFirst {
			//: stop; the later rules and the nested plan never run.
			return found, true
		}
		//: collect and keep going.
		report = append(report, found...)
	}
	//: every rule ran.
	return report, false
}
