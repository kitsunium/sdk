// Package validation — the struct-tag compiler. Everything here runs once per
// type, at construction; nothing here runs during a validation.
package validation

import (
	"reflect"
	"strings"

	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
)

// jsonTagName is the tag the compiler reads to name a field in a PATH.
//
// It is json rather than the Go field name because of a fact about this SDK,
// not a preference: internal/service/config.Load decodes every format — TOML,
// YAML, env, JSON — through a json.Marshal/json.Unmarshal round trip, so the
// json tag is literally the key the operator wrote in their file. A path built
// from Go field names would name something the operator never typed.
const jsonTagName string = "json"

// tagSeparator splits the rules inside one validate tag.
const tagSeparator string = ","

// diveRule descends into a nested struct, or into the elements of a slice.
const diveRule string = "dive"

// compileStruct builds the plan for one struct type. visiting carries the
// types currently being compiled so a self-referential type is refused rather
// than compiled forever.
func compileStruct(typ reflect.Type, stopAtFirst bool, visiting map[reflect.Type]bool) (plan *structPlan, err error) {
	//: a type that reaches itself through dive would recurse without end.
	if visiting[typ] {
		//: name the type so the cycle is obvious from the error alone.
		return nil, rejectRule(typ.String(), diveRule,
			"the type dives into itself; a recursive structure has no compile-time depth")
	}
	//: mark, and unmark on the way out so a diamond (two fields of the same
	//: type) still compiles — only a CYCLE is refused.
	visiting[typ] = true
	//: leaving this type's subtree.
	defer delete(visiting, typ)
	//: the compiled steps, in declaration order.
	steps := make([]planStep, 0, typ.NumField())
	//: every field is examined; only tagged ones contribute.
	for index := range typ.NumField() {
		//: the field's static description.
		field := typ.Field(index)
		//: an untagged field carries no rule — skipping it is what makes a
		//: type with no validate tag compile to an empty, accepting plan.
		raw := field.Tag.Get(tagName)
		//: "-" is the conventional opt-out and is treated as absent.
		if raw == "" || raw == "-" {
			//: nothing to compile here.
			continue
		}
		//: compile this field's rules and descents.
		fieldSteps, fieldErr := compileField(field, index, raw, stopAtFirst, visiting)
		//: a refused tag refuses the whole type.
		if fieldErr != nil {
			//: hand the typed refusal back.
			return nil, fieldErr
		}
		//: contribute.
		steps = append(steps, fieldSteps...)
	}
	//: every rule compiled; now refuse one no JSON key could ever feed.
	if reachErr := checkJSONReach(typ); reachErr != nil {
		//: InvalidRule, naming the field its key does not reach.
		return nil, reachErr
	}
	//: the plan, with its mode baked in.
	return &structPlan{steps: steps, stopAtFirst: stopAtFirst}, nil
}

// compileField turns one field's validate tag into zero, one or two steps: the
// field's own rules, and its descent.
func compileField(field reflect.StructField, index int, raw string, stopAtFirst bool, visiting map[reflect.Type]bool) (steps []planStep, err error) {
	//: the name this field carries in a path, and in every refusal.
	name := pathName(field)
	//: the segment it adds to a path, which is the name — except for an
	//: embedding JSON promotes, whose members are keys of the object that
	//: embeds it and are located under that object's own path.
	segment := name
	//: see promotedEmbedding.
	if promotedEmbedding(field) {
		//: JoinField treats an empty member as "no descent happened".
		segment = ""
	}
	//: rules before dive apply to the field; rules after it apply to elements.
	head, tail, hasDive := splitDive(parseTag(raw))
	//: compile the field's own rules against its static type.
	checks, checkErr := compileChecks(name, field.Type, head)
	//: a refused rule refuses the field.
	if checkErr != nil {
		//: hand the typed refusal back.
		return nil, checkErr
	}
	//: rules after dive without a dive would silently never run.
	if !hasDive && len(tail) != 0 {
		//: unreachable by construction of splitDive, but stated rather than
		//: assumed — the whole package exists to refuse the silent case.
		return nil, rejectRule(name, diveRule, "rules follow dive but no dive is present")
	}
	//: the field's own rules, if it has any.
	if len(checks) != 0 {
		//: one step per field keeps stop-at-first exact.
		steps = append(steps, fieldStep(index, segment, checks, stopAtFirst))
	}
	//: no descent requested.
	if !hasDive {
		//: done.
		return steps, nil
	}
	//: compile the descent.
	diveStep, diveErr := compileDive(field.Type, name, segment, index, diveSpec{
		tail: tail, stopAtFirst: stopAtFirst, visiting: visiting,
	})
	//: a refused descent refuses the field.
	if diveErr != nil {
		//: hand the typed refusal back.
		return nil, diveErr
	}
	//: field rules first, then the descent — so a "mincount=1" violation is
	//: reported before the element violations it explains.
	return append(steps, diveStep), nil
}

// compileChecks compiles a list of rule items against one static type.
func compileChecks(name string, typ reflect.Type, items []string) (checks []fieldCheck, err error) {
	//: nothing to compile.
	if len(items) == 0 {
		//: an empty rule list is legitimate; it contributes no check.
		return nil, nil
	}
	//: exact capacity.
	checks = make([]fieldCheck, 0, len(items))
	//: declaration order is evaluation order and therefore report order.
	for _, item := range items {
		//: one rule, bound to this field's kind at compile time.
		check, itemErr := compileRule(name, typ, item)
		//: a refused rule refuses the field.
		if itemErr != nil {
			//: hand the typed refusal back.
			return nil, itemErr
		}
		//: contribute.
		checks = append(checks, check)
	}
	//: the compiled checks.
	return checks, nil
}

// fieldStep runs one field's compiled checks, located under base extended by
// segment.
func fieldStep(index int, segment string, checks []fieldCheck, stopAtFirst bool) planStep {
	//: the closure captures the resolved index — a validation performs no
	//: field-name lookup.
	return func(base string, structValue reflect.Value) corevalidation.ReportValue {
		//: the field's path, in the core grammar.
		path := corevalidation.JoinField(base, segment)
		//: the field's value, by index.
		fieldValue := structValue.Field(index)
		//: nil report — an accepting field allocates nothing.
		var report corevalidation.ReportValue
		//: rules run in tag order.
		for _, check := range checks {
			//: evaluate.
			found := check(path, fieldValue)
			//: accepted.
			if len(found) == 0 {
				//: next rule.
				continue
			}
			//: a single check reports at most one violation, so this is exact.
			if stopAtFirst {
				//: stop; the later rules on this field never run.
				return found
			}
			//: collect and keep going.
			report = append(report, found...)
		}
		//: everything this field got wrong.
		return report
	}
}

// pathName is the name this field carries in a violation path: its json tag
// when it has one, else its Go name. See jsonTagName for why.
func pathName(field reflect.StructField) string {
	//: no json tag — the Go name is the only name there is.
	tag := field.Tag.Get(jsonTagName)
	//: fall back immediately.
	if tag == "" {
		//: the Go name.
		return field.Name
	}
	//: the name is everything before the first option ("name,omitempty").
	name, _, _ := strings.Cut(tag, tagSeparator)
	//: an empty name ("json:",omitempty"") or the opt-out ("json:"-"") leaves
	//: no wire name to use.
	if name == "" || name == "-" {
		//: the Go name.
		return field.Name
	}
	//: the wire name the operator actually typed.
	return name
}

// promotedEmbedding reports whether field is an embedding JSON PROMOTES: its
// members become members of the enclosing object and the embedding itself has
// no key, so a path that named it would name something no input contains.
//
// It is encoding/json's own rule, because JSON is what decides the keys an
// operator writes (see jsonTagName): an anonymous field is promoted when its
// json tag gives it no name and its type is a struct or a pointer to one. A
// json name makes it an ordinary member; json:"-" takes it off the wire
// altogether, so there is no promoted key to follow either; and an embedded
// non-struct is keyed by its type name. pathName already names all three.
func promotedEmbedding(field reflect.StructField) bool {
	//: only an embedding can be promoted.
	if !field.Anonymous {
		//: an ordinary member.
		return false
	}
	//: the name is everything before the first option, as in pathName.
	name, _, _ := strings.Cut(field.Tag.Get(jsonTagName), tagSeparator)
	//: a json name, or the opt-out, keeps the field out of promotion.
	if name != "" {
		//: keyed by that name, or not on the wire at all.
		return false
	}
	//: JSON follows one pointer to find the struct, as derefType does.
	target, _ := derefType(field.Type)
	//: only a struct has members to promote.
	return target.Kind() == reflect.Struct
}

// parseTag splits a validate tag into its rule items, dropping empties so a
// trailing comma is a typo rather than a failure.
func parseTag(raw string) []string {
	//: split on the separator.
	parts := strings.Split(raw, tagSeparator)
	//: exact upper bound on capacity.
	items := make([]string, 0, len(parts))
	//: keep order — it is evaluation order.
	for _, part := range parts {
		//: surrounding space is formatting, not meaning.
		trimmed := strings.TrimSpace(part)
		//: an empty item carries no rule.
		if trimmed == "" {
			//: skip it.
			continue
		}
		//: contribute.
		items = append(items, trimmed)
	}
	//: the rule items.
	return items
}

// splitDive divides the rule items at the dive marker. Items before it apply
// to the field; items after it apply to what the descent reaches.
func splitDive(items []string) (head, tail []string, hasDive bool) {
	//: find the marker.
	for index, item := range items {
		//: the marker takes no argument.
		if item == diveRule {
			//: everything before, everything after.
			return items[:index], items[index+1:], true
		}
	}
	//: no descent.
	return items, nil, false
}

// splitPresence divides the rule items that follow dive into the presence rule
// and everything else — but only for a POINTER element, where the two ask about
// different values: presence about the pointer, which is exactly what a nil
// element lacks, and the rest about the value behind it. For any other element
// both ask about the same value, so the items stay one list, in tag order.
func splitPresence(items []string, pointer bool) (presence, rest []string) {
	//: one value to ask about, one list.
	if !pointer {
		//: declaration order is evaluation order.
		return nil, items
	}
	//: route each item by its rule name; the rule compiler still sees every
	//: item, so a malformed `required=x` is refused exactly as before.
	for _, item := range items {
		//: the rule is everything before the argument.
		rule, _, _ := strings.Cut(item, argSeparator)
		//: presence is asked of the pointer.
		if rule == tagRequired {
			//: in tag order among its own kind.
			presence = append(presence, item)
			//: next item.
			continue
		}
		//: everything else is asked of the value behind it.
		rest = append(rest, item)
	}
	//: both halves, each in tag order.
	return presence, rest
}
