// Package validation — compiling the `dive` descent.
//
// Descent is EXPLICIT. A nested struct is walked only when its field says
// dive, never automatically: automatic recursion would walk into time.Time,
// net.IP and every other struct that happens to be a field, and a rule engine
// that silently reaches places the author did not name is a rule engine whose
// report cannot be trusted.
package validation

import (
	"reflect"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// diveSpec groups what compileDive needs beyond the field itself, keeping the
// function under the SDK's 5-parameter ceiling.
type diveSpec struct {
	tail        []string
	stopAtFirst bool
	visiting    map[reflect.Type]bool
}

// compileDive builds the descent step for one field.
func compileDive(typ reflect.Type, name string, index int, spec diveSpec) (step planStep, err error) {
	//: resolve one level of pointer so *Address dives like Address.
	target, deref := derefType(typ)
	//: dispatch on what there is to descend into.
	switch target.Kind() {
	//: a struct has members; the descent runs its own compiled plan.
	case reflect.Struct:
		//: a struct has members, not elements — element rules make no sense.
		if len(spec.tail) != 0 {
			//: name the fix rather than guessing at the intent.
			return nil, rejectRule(name, diveRule,
				"a struct has no elements; put the rule in the nested type's own validate tag")
		}
		//: compile the nested type inside the same visiting set, so a cycle
		//: through this field is caught rather than followed.
		nested, nestedErr := compileStruct(target, spec.stopAtFirst, spec.visiting)
		//: a refused nested tag refuses this type.
		if nestedErr != nil {
			//: hand the typed refusal back.
			return nil, nestedErr
		}
		//: descend into the struct.
		return structDiveStep(index, name, nested, deref), nil
	//: a collection has elements, located by index.
	case reflect.Slice, reflect.Array:
		//: elements are located by index, which is the other half of the path
		//: grammar.
		return compileElements(target, name, index, spec)
	//: a map has entries, but no order and no path rendering — see Deferred.
	case reflect.Map:
		//: refused BY NAME — see the doc on ADR 0046 §Deferred: a map key needs
		//: a total order for the report to be stable, and an arbitrary key type
		//: has no unambiguous rendering in the path grammar.
		return nil, rejectRule(name, diveRule,
			"dive into a map is not supported; validate the map in code with a hand-written Constraint")
	//: anything else has no inside.
	default:
		//: a scalar has nothing inside it.
		return nil, rejectRule(name, diveRule,
			"dive requires a struct, a slice or an array; this field is a "+target.Kind().String())
	}
}

// compileElements builds the descent step for a slice or array field.
func compileElements(typ reflect.Type, name string, index int, spec diveSpec) (step planStep, err error) {
	//: the element's static type, with one level of pointer resolved.
	elemType, elemDeref := derefType(typ.Elem())
	//: element rules compile against the ELEMENT's type, not the slice's.
	checks, checkErr := compileChecks(name, elemType, spec.tail)
	//: a refused element rule refuses the field.
	if checkErr != nil {
		//: hand the typed refusal back.
		return nil, checkErr
	}
	//: an element that is itself a struct gets its own plan, so a slice of
	//: structs produces "addresses[2].zip" rather than "addresses[2]".
	plan := elementPlan{checks: checks, deref: elemDeref, stopAtFirst: spec.stopAtFirst}
	//: only a struct element has a nested plan.
	if elemType.Kind() == reflect.Struct {
		//: compile it inside the same visiting set.
		nested, nestedErr := compileStruct(elemType, spec.stopAtFirst, spec.visiting)
		//: a refused nested tag refuses this type.
		if nestedErr != nil {
			//: hand the typed refusal back.
			return nil, nestedErr
		}
		//: attach.
		plan.nested = nested
	}
	//: descend into every element.
	return elementsStep(index, name, plan, spec.stopAtFirst), nil
}

// structDiveStep descends into a struct (or non-nil *struct) field.
func structDiveStep(index int, name string, nested *structPlan, deref bool) planStep {
	//: the closure captures the resolved index and the nested plan.
	return func(base string, structValue reflect.Value) corevalidation.ReportValue {
		//: the nested value.
		fieldValue := structValue.Field(index)
		//: a nil pointer holds nothing to be wrong about; requiring it to be
		//: present is `required`, a different question asked at the field.
		if deref {
			//: nothing to descend into.
			if fieldValue.IsNil() {
				//: accepted vacuously.
				return nil
			}
			//: follow the pointer.
			fieldValue = fieldValue.Elem()
		}
		//: run the nested plan under the field's own path.
		return nested.run(corevalidation.JoinField(base, name), fieldValue)
	}
}

// elementsStep descends into every element of a slice or array field.
func elementsStep(index int, name string, plan elementPlan, stopAtFirst bool) planStep {
	//: the closure captures the resolved index and the element plan.
	return func(base string, structValue reflect.Value) corevalidation.ReportValue {
		//: the slice's own path; each element extends it with its index.
		sliceBase := corevalidation.JoinField(base, name)
		//: the slice value.
		fieldValue := structValue.Field(index)
		//: nil report — an accepting slice allocates nothing.
		var report corevalidation.ReportValue
		//: index order is report order, so two runs produce the same report.
		for position := range fieldValue.Len() {
			//: everything this element got wrong.
			found := plan.check(corevalidation.JoinIndex(sliceBase, position), fieldValue.Index(position))
			//: accepted.
			if len(found) == 0 {
				//: next element.
				continue
			}
			//: a stop-at-first element plan reports exactly one violation.
			if stopAtFirst {
				//: stop; the later elements are never read.
				return found
			}
			//: collect and keep going — an import that stops at row 3 makes
			//: the operator run it six times.
			report = append(report, found...)
		}
		//: everything the slice got wrong, in index order.
		return report
	}
}

// derefType resolves one level of pointer, reporting whether it did.
func derefType(typ reflect.Type) (target reflect.Type, deref bool) {
	//: exactly one level: **T is refused downstream as "not a struct", which
	//: is honest — a pointer to a pointer in a validated schema is a design
	//: question the SDK must not answer silently.
	if typ.Kind() == reflect.Pointer {
		//: the pointee.
		return typ.Elem(), true
	}
	//: not a pointer.
	return typ, false
}
