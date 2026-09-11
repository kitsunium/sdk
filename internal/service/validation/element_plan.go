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
type elementPlan struct {
	checks      []fieldCheck
	nested      *structPlan
	deref       bool
	stopAtFirst bool
}

// check runs one element's rules and its nested plan.
func (p elementPlan) check(path string, element reflect.Value) corevalidation.ReportValue {
	//: a nil *Element holds nothing to be wrong about.
	if p.deref {
		//: skip it.
		if element.IsNil() {
			//: accepted vacuously.
			return nil
		}
		//: follow the pointer.
		element = element.Elem()
	}
	//: nil report — an accepting element allocates nothing.
	var report corevalidation.ReportValue
	//: the element's own rules first.
	for _, check := range p.checks {
		//: evaluate.
		found := check(path, element)
		//: accepted.
		if len(found) == 0 {
			//: next rule.
			continue
		}
		//: a single check reports at most one violation, so this is exact.
		if p.stopAtFirst {
			//: stop; the later rules and the nested plan never run.
			return found
		}
		//: collect and keep going.
		report = append(report, found...)
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
