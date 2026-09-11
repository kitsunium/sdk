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
//
// Nothing in it may flow into a step closure. Go's escape analysis is
// field-insensitive on a struct parameter, so one field captured by the
// closure a step returns makes the whole spec leak — visiting included, which
// then moves the caller's map to the heap: two allocations per compiled type,
// measured. What a step keeps travels as its own parameter instead.
type diveSpec struct {
	tail        []string
	stopAtFirst bool
	visiting    map[reflect.Type]bool
}

// compileDive builds the descent step for one field.
//
// name identifies the field in a refusal, where "the field" is what a
// developer is looking for; segment is what the field adds to a violation
// path, which is not always its name — an embedding JSON promotes adds nothing
// (see promotedEmbedding).
func compileDive(typ reflect.Type, name, segment string, index int, spec diveSpec) (step planStep, err error) {
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
		return structDiveStep(index, segment, nested, deref), nil
	//: a collection has elements, located by index.
	case reflect.Slice, reflect.Array:
		//: the rule set every element is checked against.
		plan, planErr := compileElements(target, name, spec)
		//: a refused element rule refuses the field.
		if planErr != nil {
			//: hand the typed refusal back.
			return nil, planErr
		}
		//: elements are located by index, which is the other half of the path
		//: grammar. The pointer resolved above travels with them: a step that
		//: forgot it would call Len on the pointer and panic on every
		//: validation of a field that compiled without complaint.
		return elementsStep(index, segment, plan, deref), nil
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

// compileElements compiles the rule set every element of a slice or array type
// is checked against.
//
// When the ELEMENT is a pointer, presence is compiled apart from the other
// element rules and asked of the pointer itself. `required` then means what it
// means on a pointer field and what Each(…, Required[*T]()) means in code: the
// element is there, i.e. not nil — while a nil element gives every other rule
// nothing to be wrong about.
func compileElements(typ reflect.Type, name string, spec diveSpec) (plan elementPlan, err error) {
	//: the element's static type, with one level of pointer resolved.
	elemType, elemDeref := derefType(typ.Elem())
	//: presence and the rest ask about different values when the element is
	//: a pointer; for any other element they stay one list, in tag order.
	presenceItems, valueItems := splitPresence(spec.tail, elemDeref)
	//: presence compiles against the element as it is stored.
	presence, presenceErr := compileChecks(name, typ.Elem(), presenceItems)
	//: a refused element rule refuses the field.
	if presenceErr != nil {
		//: hand the typed refusal back.
		return elementPlan{}, presenceErr
	}
	//: element rules compile against the ELEMENT's type, not the slice's.
	checks, checkErr := compileChecks(name, elemType, valueItems)
	//: a refused element rule refuses the field.
	if checkErr != nil {
		//: hand the typed refusal back.
		return elementPlan{}, checkErr
	}
	//: an element that is itself a struct gets its own plan, so a slice of
	//: structs produces "addresses[2].zip" rather than "addresses[2]".
	plan = elementPlan{presence: presence, checks: checks, deref: elemDeref, stopAtFirst: spec.stopAtFirst}
	//: only a struct element has a nested plan.
	if elemType.Kind() == reflect.Struct {
		//: compile it inside the same visiting set.
		nested, nestedErr := compileStruct(elemType, spec.stopAtFirst, spec.visiting)
		//: a refused nested tag refuses this type.
		if nestedErr != nil {
			//: hand the typed refusal back.
			return elementPlan{}, nestedErr
		}
		//: attach.
		plan.nested = nested
	}
	//: the element rule set.
	return plan, nil
}

// structDiveStep descends into a struct (or non-nil *struct) field, located
// under base extended by segment.
func structDiveStep(index int, segment string, nested *structPlan, deref bool) planStep {
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
		//: run the nested plan under the field's own path — its parent's, for
		//: an embedding JSON promotes.
		return nested.run(corevalidation.JoinField(base, segment), fieldValue)
	}
}

// elementsStep descends into every element of a slice or array field, located
// under base extended by segment. With deref set the field is a POINTER to the
// collection, and it follows the one-level pointer rule structDiveStep does.
func elementsStep(index int, segment string, plan elementPlan, deref bool) planStep {
	//: the closure captures the resolved index and the element plan.
	return func(base string, structValue reflect.Value) corevalidation.ReportValue {
		//: the slice value, or the pointer to it.
		fieldValue := structValue.Field(index)
		//: a pointer to a collection is followed before anything measures it.
		if deref {
			//: a nil pointer holds nothing to be wrong about; requiring it to
			//: be present is `required`, a different question asked at the
			//: field.
			if fieldValue.IsNil() {
				//: accepted vacuously.
				return nil
			}
			//: the collection itself.
			fieldValue = fieldValue.Elem()
		}
		//: the slice's own path; each element extends it with its index.
		sliceBase := corevalidation.JoinField(base, segment)
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
			if plan.stopAtFirst {
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
