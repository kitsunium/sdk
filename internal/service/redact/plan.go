// Package redact — which members of a Go type's JSON form are secret by
// declaration, worked out once per type.
package redact

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
)

// plan says, for one Go type, which members of its JSON form are secret by
// declaration, and the plans of the members below them. A nil plan declares
// nothing: only names are then judged.
type plan struct {
	members map[string]*plan
	items   *plan // an array's elements, a map's values
	secret  bool
}

// member returns the plan of an object member: a struct's field by its JSON
// name, or a map's value whatever its key.
func (p *plan) member(name string) *plan {
	//: nothing declared below here.
	if p == nil {
		//: names only.
		return nil
	}
	//: a map: every value shares one plan.
	if p.members == nil {
		//: the element plan.
		return p.items
	}
	//: a struct: the field of that JSON name, if it declared anything.
	return p.members[name]
}

// item returns the plan of an array's elements.
func (p *plan) item() *plan {
	//: nothing declared below here.
	if p == nil {
		//: names only.
		return nil
	}
	//: the element plan.
	return p.items
}

// Types whose JSON form reflection cannot see.
var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// planOf returns the plan of t, from the cache when t was seen before.
func (r *Redactor) planOf(t reflect.Type) *plan {
	//: a nil interface has no type and declares nothing.
	if t == nil {
		//: names only.
		return nil
	}
	//: the common case: the type was seen before.
	if cached, found := r.plans.Load(t); found {
		//: only plans are ever stored.
		if known, isPlan := cached.(*plan); isPlan {
			//: as built the first time.
			return known
		}
	}
	built := r.build(t, map[reflect.Type]*plan{})
	//: two goroutines building the same type build the same plan; the one
	//: stored second is simply discarded, and both return the one kept.
	stored, _ := r.plans.LoadOrStore(t, built)
	//: only plans are ever stored.
	if kept, isPlan := stored.(*plan); isPlan {
		//: the plan every later call reads.
		return kept
	}
	//: unreachable: only plans are ever stored.
	return built
}

// writesOwnJSON reports whether t writes its own JSON, whose shape reflection
// cannot see: only its names can then be judged.
func writesOwnJSON(t reflect.Type) bool {
	//: a value or a pointer receiver.
	for _, marshaler := range []reflect.Type{jsonMarshalerType, textMarshalerType} {
		//: either receiver makes encoding/json call it.
		if t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler) {
			//: opaque.
			return true
		}
	}
	//: laid out by reflection.
	return false
}

// build walks t the way encoding/json lays it out. seen holds the plans being
// built, so a recursive type shares its own plan instead of looping.
func (r *Redactor) build(t reflect.Type, seen map[reflect.Type]*plan) *plan {
	//: a pointer's JSON form is its element's.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	//: opaque to reflection.
	if writesOwnJSON(t) {
		//: names only.
		return nil
	}
	switch t.Kind() {
	//: an object whose members are the fields.
	case reflect.Struct:
		//: a recursive type: the plan under construction.
		if building, found := seen[t]; found {
			//: shared, not rebuilt.
			return building
		}
		built := &plan{members: map[string]*plan{}}
		seen[t] = built
		r.fields(built, t, seen)
		//: the struct's plan.
		return built
	//: an array, unless it is the bytes encoding/json writes as base64.
	case reflect.Slice, reflect.Array:
		//: []byte is a string on the wire.
		if t.Elem().Kind() == reflect.Uint8 {
			//: names only.
			return nil
		}
		//: the elements' plan, when they declare anything.
		return r.below(t.Elem(), seen)
	//: an object whose members are the entries.
	case reflect.Map:
		//: the values' plan, when they declare anything.
		return r.below(t.Elem(), seen)
	//: a scalar declares nothing.
	default:
		//: names only.
		return nil
	}
}

// below builds the plan shared by every element of an array or value of a
// map, or nil when the element declares nothing.
func (r *Redactor) below(element reflect.Type, seen map[reflect.Type]*plan) *plan {
	//: nothing declared below: no plan at all, so the copy stays on names.
	if inner := r.build(element, seen); inner != nil {
		//: one plan for every element.
		return &plan{items: inner}
	}
	//: names only.
	return nil
}

// fields adds to p the plan of every member encoding/json writes for struct t:
// for each member name, the plan of the one field encoding/json itself selects
// (writtenFields). A member that declares nothing keeps no plan, so only its
// name is judged. seen holds the plans being built, so a recursive type shares
// its own plan instead of walking forever.
func (r *Redactor) fields(p *plan, t reflect.Type, seen map[reflect.Type]*plan) {
	//: one member per name, the field encoding/json writes under it.
	for _, written := range writtenFields(t) {
		//: a member that declares nothing keeps no plan.
		if member := r.memberPlan(written.field, seen); member != nil {
			p.members[written.name] = member
		}
	}
}

// memberPlan is the plan of one member: the whole member replaced when the
// field is secret by declaration, else whatever its own type declares below
// it — nil when that is nothing.
func (r *Redactor) memberPlan(field reflect.StructField, seen map[reflect.Type]*plan) *plan {
	//: secret by declaration: the whole member is replaced.
	if r.declared(field) {
		//: replaced, whatever it holds.
		return &plan{secret: true}
	}
	//: a member whose own type declares something below it.
	return r.build(field.Type, seen)
}

// declared reports whether a field is secret by declaration: the Redactor's
// tag carries the "secret" option, or its extra field rule says so.
func (r *Redactor) declared(field reflect.StructField) bool {
	//: `tag:"secret"` or `tag:"other,secret"`.
	for option := range strings.SplitSeq(field.Tag.Get(r.tag), ",") {
		//: the one option this package reads.
		if strings.TrimSpace(option) == secretOption {
			//: declared secret.
			return true
		}
	}
	//: the caller's own rule, when there is one.
	return r.field != nil && r.field(field)
}
