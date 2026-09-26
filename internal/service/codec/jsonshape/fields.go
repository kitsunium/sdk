// Package jsonshape — the members encoding/json writes for a struct, resolved
// by its own rules: json/v2's field walk under the v1 options Go 1.27's
// encoding/json runs with.
package jsonshape

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
)

// member is one field encoding/json writes as an object member. It is
// handled by pointer: it is too large to copy through every sort comparison.
type member struct {
	name   string
	goName string
	typ    reflect.Type
	tag    reflect.StructTag
	index  []int
	opts   tagOptions
}

// level is a struct whose fields join the object being resolved: the struct
// itself, or one it embeds, with the index path that reaches it. children
// says whether the structs it embeds are explored in turn — a struct type met
// a second time lends its own fields again, so a repeat can tie and cancel
// out, but not its embeddings, so a cycle ends.
type level struct {
	typ      reflect.Type
	index    []int
	children bool
}

// fieldWalk is the breadth-first walk over one struct and the structs it
// embeds: the levels still to scan, the struct types met so far, and what the
// scanned fields became.
type fieldWalk struct {
	queue     []level
	seen      map[reflect.Type]bool
	found     []*member
	fallbacks []*member
}

// members returns the members encoding/json writes for struct t, in the order
// it writes them, and the type of the embedded map or jsontext.Value that
// collects any other member, or nil.
//
// Embedded structs are explored breadth first, so a field's depth is its
// index length. For one name, only the shallowest fields compete: one wins
// alone, or a single tagged one among several; otherwise none is written —
// and no deeper field of that name either.
func members(t reflect.Type) (written []*member, extra reflect.Type) {
	walk := fieldWalk{queue: []level{{typ: t, children: true}}, seen: map[reflect.Type]bool{t: true}}
	//: breadth first: scanning a struct may queue the structs it embeds.
	for len(walk.queue) > 0 {
		current := walk.queue[0]
		walk.queue = walk.queue[1:]
		walk.scan(current)
	}
	//: the shallowest single fallback takes the extra members.
	if dominant := dominantFallback(walk.fallbacks); dominant != nil {
		extra = indirect(dominant.typ)
	}
	return dominantMembers(walk.found), extra
}

// scan adds the fields of one struct, in declaration order.
func (w *fieldWalk) scan(current level) {
	//: every field of the struct.
	for fieldIndex := range current.typ.NumField() {
		w.add(current, current.typ.Field(fieldIndex), fieldIndex)
	}
}

// add decides what one field becomes: nothing, a promoted struct, a collector
// of unknown members, or a member.
func (w *fieldWalk) add(current level, field reflect.StructField, fieldIndex int) {
	opts, ignored := parseTag(field)
	//: never written: `json:"-"`, or unexported and not embedded.
	if ignored {
		return
	}
	candidate := &member{
		name: opts.name, goName: field.Name, typ: field.Type, tag: field.Tag,
		index: append(slices.Clone(current.index), fieldIndex), opts: opts,
	}
	//: an embedded struct with no name of its own is promoted.
	if field.Anonymous && !opts.hasName && indirect(field.Type).Kind() == reflect.Struct {
		candidate.opts.embed = true
	}
	//: an embedding that promoted, collected or dropped the field.
	if candidate.opts.embed && w.embedded(current, field, candidate) {
		return
	}
	//: an ordinary member, if encoding/json can write it at all.
	if writable(field, candidate.opts) {
		w.found = append(w.found, candidate)
	}
}

// embedded handles an embedded field — anonymous, or named with the embed
// option — and reports whether it was consumed: promoted into the next level,
// kept as the collector of unknown members, or dropped. A field it does not
// consume stands as an ordinary member.
func (w *fieldWalk) embedded(current level, field reflect.StructField, candidate *member) bool {
	promoted, collects, standsAsMember := embed(field, candidate)
	//: what the embedding became.
	switch {
	//: its fields join the next level — explored only from a struct whose
	//: children are.
	case promoted:
		target := indirect(field.Type)
		//: a struct met again lends its fields, not its embeddings.
		if current.children {
			w.queue = append(w.queue, level{typ: target, index: candidate.index, children: !w.seen[target]})
		}
		w.seen[target] = true
		return true
	//: a map or a jsontext.Value that takes the unknown members.
	case collects:
		w.fallbacks = append(w.fallbacks, candidate)
		return true
	//: nothing json/v2 can embed: a member, or nothing at all.
	default:
		return !standsAsMember
	}
}

// embed decides what an embedded field — anonymous, or named with the embed
// option — becomes: promoted, when it is a struct; the collector of unknown
// members, when it is an exported map with string keys or a jsontext.Value;
// otherwise an ordinary member (standsAsMember) or nothing at all.
//
// A field carrying options other than embed is embedded all the same, with
// those options dropped — unless it names itself, which makes it an ordinary
// member under that name.
func embed(field reflect.StructField, candidate *member) (promoted, collects, standsAsMember bool) {
	//: options besides embed: a named field is a member after all.
	if candidate.opts.hasOthers() {
		//: `json:"name,embed"`, or a tagged anonymous struct: a member.
		if candidate.opts.hasName {
			return false, false, true
		}
		candidate.opts = tagOptions{name: candidate.opts.name, embed: true}
	}
	target := indirect(field.Type)
	//: a struct: its fields are this object's.
	if target.Kind() == reflect.Struct {
		return true, false, false
	}
	//: an unexported non-struct embeds nothing and is not a member.
	if !field.IsExported() {
		return false, false, false
	}
	//: the two collectors json/v2 accepts.
	if isJSONTextValue(target) || (target.Kind() == reflect.Map && target.Key().Kind() == reflect.String && !hasAnyMethod(target.Key())) {
		return false, true, false
	}
	//: anything else it cannot embed: an ordinary member.
	return false, false, true
}

// writable reports whether an ordinary member is written: every exported
// field is, and so is an unexported embedded struct given a name — unless it
// has methods encoding/json could not call on an unexported field.
func writable(field reflect.StructField, opts tagOptions) bool {
	//: an exported field.
	if field.IsExported() {
		return true
	}
	target := indirect(field.Type)
	//: an unexported field that is not an embedded struct.
	if !field.Anonymous || target.Kind() != reflect.Struct {
		return false
	}
	//: methods on an unexported field cannot be called.
	return !hasAnyMethod(target) && (!opts.omitZero || !receives(target, isZeroerType, true))
}

// dominantMembers keeps, for each name, the member encoding/json writes, and
// returns them in the order it writes them: depth first, by index path.
func dominantMembers(found []*member) []*member {
	byName := slices.Clone(found)
	slices.SortStableFunc(byName, func(a, b *member) int {
		//: grouped by name, the shallowest first, a tagged one before an untagged one.
		return cmp.Or(strings.Compare(a.name, b.name), cmp.Compare(len(a.index), len(b.index)), compareTagged(a, b))
	})
	written := make([]*member, 0, len(byName))
	//: one group of candidates per name.
	for start := 0; start < len(byName); {
		end := start + 1
		//: the candidates sharing the first one's name.
		for end < len(byName) && byName[end].name == byName[start].name {
			end++
		}
		first := byName[start]
		//: alone, or shallower, or alone in being tagged: the one written.
		if end-start == 1 || len(byName[start+1].index) != len(first.index) || byName[start+1].opts.hasName != first.opts.hasName {
			written = append(written, first)
		}
		start = end
	}
	slices.SortFunc(written, func(a, b *member) int {
		//: the order encoding/json writes them in.
		return slices.Compare(a.index, b.index)
	})
	return written
}

// compareTagged orders a member named by its tag before one named by its Go
// name.
func compareTagged(a, b *member) int {
	//: tagged before untagged, else equal.
	switch {
	//: tagged first.
	case a.opts.hasName && !b.opts.hasName:
		return -1
	//: untagged second.
	case !a.opts.hasName && b.opts.hasName:
		return 1
	//: equal.
	default:
		return 0
	}
}

// dominantFallback returns the collector of unknown members json/v2 uses: the
// only one, or the one shallower than every other — nil when none is, or two
// tie at one depth.
func dominantFallback(fallbacks []*member) *member {
	//: none, or two at one depth: nothing collects.
	if len(fallbacks) == 0 || (len(fallbacks) > 1 && len(fallbacks[0].index) == len(fallbacks[1].index)) {
		return nil
	}
	//: found breadth first, so the first is the shallowest.
	return fallbacks[0]
}

// optional reports whether an encoded object can lack the member: omitzero;
// omitempty on a kind the v1 definition of empty covers; or a path to the
// field through an embedded pointer, which nil drops the member with.
func (m *member) optional(root reflect.Type) bool {
	//: dropped when zero whatever the kind, or when empty on a kind that can be.
	if m.opts.omitZero || (m.opts.omitEmpty && emptiable(m.typ)) {
		return true
	}
	//: dropped when an embedding on the way is a nil pointer.
	return throughPointer(root, m.index)
}

// quoted reports whether the ",string" option takes effect: on a bool, a
// number or a string — through one unnamed pointer — that writes nothing of
// its own where it is encoded, and on json.Number, whose own method honours
// the option. addressable is the field's, and a pointer followed makes what it
// points at addressable.
func (m *member) quoted(addressable bool) bool {
	//: the option is absent.
	if !m.opts.quoted {
		return false
	}
	target := m.typ
	//: one unnamed pointer is followed, as encoding/json follows it.
	if target.Name() == "" && target.Kind() == reflect.Pointer {
		target, addressable = target.Elem(), true
	}
	//: json.Number writes itself, and quotes itself under the option.
	if target == numberType {
		return true
	}
	//: any other type writing its own JSON or text there ignores the option.
	if writesJSON(target, addressable) || writesText(target, addressable) {
		return false
	}
	//: the kinds the option applies to.
	switch target.Kind() {
	//: the kinds the option applies to.
	case reflect.Bool, reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	//: every other kind ignores it.
	default:
		return false
	}
}

// emptiable reports whether a value of t can be empty under the v1 definition
// omitempty keeps: false, zero, an empty string, collection or zero-length
// array, a nil pointer or interface. A struct is never empty.
func emptiable(t reflect.Type) bool {
	//: the kinds with an empty value.
	switch t.Kind() {
	//: the kinds with an empty value.
	case reflect.Bool, reflect.String, reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	//: an array is empty only when it holds nothing.
	case reflect.Array:
		return t.Len() == 0
	//: a struct, and the kinds encoding/json refuses.
	default:
		return false
	}
}

// throughPointer reports whether the path to a promoted field passes through
// an embedded pointer — a nil one drops every field below it.
func throughPointer(root reflect.Type, index []int) bool {
	current := root
	//: every embedding on the way, not the field itself.
	for _, step := range index[:len(index)-1] {
		embedded := current.Field(step).Type
		//: a pointer on the way.
		if embedded.Kind() == reflect.Pointer {
			return true
		}
		current = embedded
	}
	return false
}

// indirect follows one unnamed pointer, as Go allows embedding *T but not **T.
func indirect(t reflect.Type) reflect.Type {
	//: *T becomes T; a named pointer type is a type of its own.
	if t.Kind() == reflect.Pointer && t.Name() == "" {
		return t.Elem()
	}
	return t
}
