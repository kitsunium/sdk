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
		r.fields(built, t, seen, map[reflect.Type]bool{t: true})
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

// fields adds the members of struct t to p, promoting the fields of embedded
// structs the way encoding/json does. embedded guards against an embedding
// cycle.
func (r *Redactor) fields(p *plan, t reflect.Type, seen map[reflect.Type]*plan, embedded map[reflect.Type]bool) {
	r.collect(p, t, seen, embedded, 0, map[string]int{})
}

// collect adds the fields of t, found depth embeddings below the struct being
// planned, to p. depthOf records how deep each member name was first found.
//
// encoding/json writes, for one name, the field found at the SHALLOWEST depth,
// whatever the declaration order — so a deeper promoted field never replaces a
// shallower member's plan, and a shallower field replaces a deeper one's, even
// a field that declares nothing (its value is the one written, and a deeper
// plan would be applied to the wrong value). Two fields at one depth are
// ambiguous: encoding/json writes neither, or the tagged one; the plan keeps
// the stricter of the two, so a secret is never lost to a tie.
func (r *Redactor) collect(p *plan, t reflect.Type, seen map[reflect.Type]*plan, embedded map[reflect.Type]bool, depth int, depthOf map[string]int) {
	//: every field, in declaration order.
	for field := range t.Fields() {
		name, promoted, visible := jsonName(field)
		//: encoding/json does not write it.
		if !visible {
			continue
		}
		//: an embedded struct without a name: its fields are this object's,
		//: one level deeper.
		if promoted {
			r.promote(p, field.Type, seen, embedded, depth+1, depthOf)
			continue
		}
		found, taken := depthOf[name]
		//: a shallower field of that name is the one written.
		if taken && found < depth {
			continue
		}
		var member *plan
		//: secret by declaration: the whole member is replaced.
		if r.declared(field) {
			member = &plan{secret: true}
		} else {
			//: a member whose own type declares something below it.
			member = r.build(field.Type, seen)
		}
		//: a tie keeps the stricter plan.
		if taken && found == depth {
			//: a secret already planned stays secret.
			if current := p.members[name]; current != nil && current.secret {
				continue
			}
			//: otherwise the new plan only when it hides something.
			if member != nil && member.secret {
				p.members[name] = member
			}
			continue
		}
		depthOf[name] = depth
		//: the shallowest field so far: its plan, or none.
		if member != nil {
			p.members[name] = member
		} else {
			delete(p.members, name)
		}
	}
}

// promote adds the fields of an embedded struct to p, one depth further.
func (r *Redactor) promote(p *plan, embedded reflect.Type, seen map[reflect.Type]*plan, visited map[reflect.Type]bool, depth int, depthOf map[string]int) {
	//: through the pointer, as encoding/json does.
	if embedded.Kind() == reflect.Pointer {
		embedded = embedded.Elem()
	}
	//: each embedded type once, so a cycle of embeddings ends.
	if visited[embedded] {
		return
	}
	visited[embedded] = true
	r.collect(p, embedded, seen, visited, depth, depthOf)
}

// jsonName returns the member name encoding/json writes field under, whether
// the field is an embedded struct whose fields are promoted instead, and
// whether it is written at all.
func jsonName(field reflect.StructField) (name string, promoted, visible bool) {
	tag := field.Tag.Get("json")
	//: `json:"-"` is never written; `json:"-,"` is a member named "-" —
	//: the whole tag is compared, exactly as encoding/json does.
	if tag == "-" {
		//: invisible.
		return "", false, false
	}
	tagName, _, _ := strings.Cut(tag, ",")
	//: an embedded field with no name of its own.
	if field.Anonymous && tagName == "" {
		embedded := field.Type
		//: through the pointer.
		if embedded.Kind() == reflect.Pointer {
			embedded = embedded.Elem()
		}
		//: a struct that lays itself out by reflection is flattened into
		//: its parent — even when its own type is unexported.
		if embedded.Kind() == reflect.Struct && !writesOwnJSON(embedded) {
			//: promoted.
			return "", true, true
		}
	}
	//: an unexported field is never written.
	if !field.IsExported() {
		//: invisible.
		return "", false, false
	}
	//: the Go name when the tag gives none.
	if tagName == "" {
		tagName = field.Name
	}
	//: a member of its own.
	return tagName, false, true
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
