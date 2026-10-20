package redact

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// plan says, for one Go type, which members of its JSON form are secret by
// declaration, and the plans of the members below them. A nil plan declares
// nothing: only names are then judged.
//
// A type's plan is built once and cached. An interface-typed member is the one
// thing a type cannot plan: what it holds is decided per value, so its plan is
// dynamicPlan, and Value resolves a plan that reaches one against the value
// itself (resolve) before the copy — that per-value plan is never cached.
type plan struct {
	members map[string]*plan
	// paths is, for a struct's plan, the index path of the field behind each
	// planned member, so resolve reaches the value encoding/json wrote.
	paths map[string][]int
	items *plan // an array's elements, a map's values
	// elements is, for an array resolved against a value, one plan per
	// element: the elements of a []any differ in type.
	elements []*plan
	secret   bool
	// dynamic marks an interface: the plan is its dynamic value's.
	dynamic bool
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

// itemAt returns the plan of an array's element at index.
func (p *plan) itemAt(index int) *plan {
	//: nothing declared below here.
	if p == nil {
		//: names only.
		return nil
	}
	//: resolved against a value: each element its own plan.
	if p.elements != nil {
		//: an element the value did not have declares nothing.
		if index >= len(p.elements) {
			//: names only.
			return nil
		}
		//: that element's plan.
		return p.elements[index]
	}
	//: the element plan every element shares.
	return p.items
}

// Types whose JSON form reflection cannot see, and the plan of every interface
// type — shared and never written: resolve replaces it with the plan of the
// value it holds.
var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	dynamicPlan       = &plan{dynamic: true}
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
		built := &plan{members: map[string]*plan{}, paths: map[string][]int{}}
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
	//: what an interface holds is decided per value (#436).
	case reflect.Interface:
		//: resolved against the value by resolve.
		return dynamicPlan
	//: a scalar declares nothing.
	default:
		//: names only.
		return nil
	}
}

// opensAt reports whether the plan declared for t reaches an interface,
// walking it once per type and caching the answer.
func (r *Redactor) opensAt(t reflect.Type, declared *plan) bool {
	//: the common case: the type was seen before.
	if cached, found := r.opens.Load(t); found {
		//: only booleans are ever stored.
		if open, isBool := cached.(bool); isBool {
			//: as found the first time.
			return open
		}
	}
	open := isOpen(declared, map[*plan]bool{})
	r.opens.Store(t, open)
	//: the plan's answer, the same for every value of t.
	return open
}

// isOpen reports whether p reaches an interface, so a value of its type must
// be resolved before its plan applies. seen holds the plans already visited,
// so a recursive type is walked once.
func isOpen(p *plan, seen map[*plan]bool) bool {
	//: nothing below, or already visited on this walk.
	if p == nil || seen[p] {
		//: nothing more to find here.
		return false
	}
	seen[p] = true
	//: an interface itself.
	if p.dynamic {
		//: open.
		return true
	}
	//: a member, or the elements, reaching one.
	for _, member := range p.members {
		//: one is enough.
		if isOpen(member, seen) {
			//: open.
			return true
		}
	}
	//: the elements' plan, the last place one can be.
	return isOpen(p.items, seen)
}

// resolve returns the plan of the value rv under the static plan p: p itself
// where nothing below it is an interface, and otherwise a plan built for this
// value, every interface replaced by the plan of the type it holds. It
// follows rv the way encoding/json wrote it — through pointers, the fields
// behind each member, every element and every map entry.
func (r *Redactor) resolve(p *plan, rv reflect.Value) *plan {
	//: nothing declared, or the whole value replaced anyway.
	if p == nil || p.secret {
		//: as planned.
		return p
	}
	//: an interface: plan the type it holds, then resolve that.
	if p.dynamic {
		//: the held value under its own type's plan.
		return r.resolveHeld(rv)
	}
	//: a pointer's JSON form is its element's.
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		//: null declares nothing below it.
		if rv.IsNil() {
			//: as planned.
			return p
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	//: each planned member, resolved against its own field.
	case reflect.Struct:
		return r.resolveStruct(p, rv)
	//: each element, resolved on its own.
	case reflect.Slice, reflect.Array:
		return r.resolveElements(p, rv)
	//: each entry, under the member name encoding/json wrote its key as.
	case reflect.Map:
		return r.resolveEntries(p, rv)
	//: a scalar: the static plan is all there is.
	default:
		//: as planned.
		return p
	}
}

// resolveHeld returns the plan of what interface value rv holds: the plan of
// its dynamic type, cached like any type's, resolved against it.
func (r *Redactor) resolveHeld(rv reflect.Value) *plan {
	//: a nil interface encodes as null and declares nothing.
	if rv.Kind() != reflect.Interface || rv.IsNil() {
		//: names only.
		return nil
	}
	held := rv.Elem()
	//: the dynamic type's own plan, resolved below it.
	return r.resolve(r.planOf(held.Type()), held)
}

// resolveElements resolves each element of array or slice value rv on its
// own: the elements of a []any differ in type.
func (r *Redactor) resolveElements(p *plan, rv reflect.Value) *plan {
	resolved := &plan{elements: make([]*plan, rv.Len())}
	//: in order, as written.
	for index := range rv.Len() {
		resolved.elements[index] = r.resolve(p.items, rv.Index(index))
	}
	//: this value's elements.
	return resolved
}

// resolveEntries resolves each entry of map value rv under the member name
// encoding/json writes its key as.
func (r *Redactor) resolveEntries(p *plan, rv reflect.Value) *plan {
	resolved := &plan{members: map[string]*plan{}}
	entries := rv.MapRange()
	//: every entry; one whose key encoding/json cannot name is not written.
	for entries.Next() {
		name, named := mapKeyName(entries.Key())
		//: a key encoding/json refuses never reaches the document.
		if !named {
			continue
		}
		//: an entry that declares nothing keeps no plan.
		if member := r.resolve(p.items, entries.Value()); member != nil {
			resolved.members[name] = member
		}
	}
	//: this value's entries.
	return resolved
}

// resolveStruct resolves each planned member of struct value rv against the
// field encoding/json wrote under its name.
func (r *Redactor) resolveStruct(p *plan, rv reflect.Value) *plan {
	resolved := &plan{members: map[string]*plan{}}
	//: only members the type planned can declare anything.
	for name, member := range p.members {
		field, unreachable := rv.FieldByIndexErr(p.paths[name])
		//: behind a nil embedded pointer: encoding/json wrote no such member.
		if unreachable != nil {
			continue
		}
		//: a member that declares nothing for this value keeps no plan.
		if inner := r.resolve(member, field); inner != nil {
			resolved.members[name] = inner
		}
	}
	//: this value's members.
	return resolved
}

// mapKeyName returns the member name encoding/json writes map key key under:
// a string as it is, a TextMarshaler's text, an integer in decimal. named is
// false for a key encoding/json cannot write.
func mapKeyName(key reflect.Value) (name string, named bool) {
	//: a string kind is used as it is, before any method.
	if key.Kind() == reflect.String {
		//: the key itself.
		return key.String(), true
	}
	//: a key that writes its own text.
	if marshaler, isText := reflect.TypeAssert[encoding.TextMarshaler](key); isText {
		text, textErr := marshaler.MarshalText()
		//: encoding/json would have refused the whole value.
		if textErr != nil {
			//: unnamed.
			return "", false
		}
		//: its text.
		return string(text), true
	}
	switch key.Kind() {
	//: a signed integer, in decimal.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(key.Int(), 10), true
	//: an unsigned integer, in decimal.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(key.Uint(), 10), true
	//: nothing encoding/json can write as a member name.
	default:
		//: unnamed.
		return "", false
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
			p.paths[written.name] = written.index
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
