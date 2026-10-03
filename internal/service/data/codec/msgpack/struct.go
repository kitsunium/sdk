// Package msgpack — how a Go struct maps onto a MessagePack map. The rules are
// the ones the vendor-backed codec applied, so existing tags keep their
// meaning:
//
//   - The key is the `msgpack:"name"` tag, else the Go field name — case and
//     all; there is no fallback to the json tag. `msgpack:"-"` leaves the
//     field out, and so does being unexported.
//   - `omitempty` leaves an empty field out of the map: a zero number, false,
//     an empty string, slice, map or array, a nil pointer or interface, a
//     value whose IsZero() reports true (time.Time), a struct all of whose
//     fields would be left out.
//   - A field named `_msgpack` carries struct-wide options: `as_array` (or
//     `asArray`) encodes the struct as an array of every field in order, and
//     `omitempty` applies to every field declared after it.
//   - An embedded struct, or pointer to one, has its fields INLINED unless one
//     of their names is already taken, `noinline` is set, or the type encodes
//     itself (a marshaler, time.Time); `inline` forces it and drops only the
//     colliding names. A struct that cannot inline is one field named after
//     it — and an inlined one's name still decodes, as a nested map.
//   - `alias:other` lets the field also be DECODED from the key "other".
//
// Decoding matches keys exactly and steps over keys no field claims; a struct
// accepts both the map and the array form whatever its own options. Two fields
// claiming one key are refused when the type is first used, instead of writing
// a map that repeats a key.
package msgpack

import (
	"encoding"
	"reflect"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Struct-tag vocabulary.
const (
	// tagKey is the struct tag this codec reads.
	tagKey string = "msgpack"
	// markerField is the field whose tag carries struct-wide options.
	markerField string = "_msgpack"
	// optOmitEmpty leaves an empty field out.
	optOmitEmpty string = "omitempty"
	// optAsArray encodes the struct as an array.
	optAsArray string = "as_array"
	// optAsArrayCamel is the camel-case spelling of optAsArray.
	optAsArrayCamel string = "asArray"
	// optInline forces an embedded struct's fields inline.
	optInline string = "inline"
	// optNoInline keeps an embedded struct as one field.
	optNoInline string = "noinline"
	// optAlias names an extra key the field decodes from.
	optAlias string = "alias"
	// tagSkip is the tag name that leaves a field out.
	tagSkip string = "-"
	// detailDuplicateKey is the failure of a type two fields share a key in.
	detailDuplicateKey string = "two struct fields claim the same MessagePack key"
)

// structField is one key of a struct's map form.
type structField struct {
	// typ is the field's Go type.
	typ reflect.Type
	// name is the key.
	name string
	// wireName is the key already encoded as a str, appended as-is.
	wireName []byte
	// index is the path from the outer struct through any inlined embeddings.
	index []int
	// omitEmpty leaves the field out when it is empty.
	omitEmpty bool
}

// structLayout is the cached map form of one struct type.
type structLayout struct {
	// dupName is a key two fields claim — a defect of the type itself,
	// reported by every encode and decode of it — or "".
	dupName string
	// byName finds the field a decoded key addresses, aliases included.
	byName map[string]*structField
	// fields are the keys in encode order.
	fields []*structField
	// asArray encodes the struct as an array.
	asArray bool
	// hasOmitEmpty is set when any field may be left out.
	hasOmitEmpty bool
}

// tagSpec is a parsed struct tag.
type tagSpec struct {
	// opts are the options after the name; a bare option maps to "".
	opts map[string]string
	// name is the key, or "" for the Go field name.
	name string
}

// layoutBuilder accumulates one struct's layout.
type layoutBuilder struct {
	// layout is the result being built.
	layout *structLayout
	// visiting holds the struct types on the current embedding path, so a
	// type that embeds itself through a pointer is not inlined forever.
	visiting map[reflect.Type]bool
	// omitAll is set once a _msgpack marker carried omitempty.
	omitAll bool
}

// Interfaces whose presence makes a type encode or decode itself.
var (
	// layouts caches *structLayout per struct type.
	layouts sync.Map
	// customCodecTypes are the interfaces that make a struct its own codec,
	// and so not a candidate for inlining.
	customCodecTypes = []reflect.Type{
		reflect.TypeFor[marshalMsgpacker](),
		reflect.TypeFor[unmarshalMsgpacker](),
		reflect.TypeFor[encoding.BinaryMarshaler](),
		reflect.TypeFor[encoding.BinaryUnmarshaler](),
		reflect.TypeFor[encoding.TextMarshaler](),
		reflect.TypeFor[encoding.TextUnmarshaler](),
	}
)

// layoutOf returns the cached layout of struct type t, building it once.
func layoutOf(t reflect.Type) *structLayout {
	//: fast path: built before.
	if cached, ok := layouts.Load(t); ok {
		//: the cache only ever holds *structLayout.
		if l, isLayout := cached.(*structLayout); isLayout {
			//: hit.
			return l
		}
	}
	l := buildLayout(t, map[reflect.Type]bool{})
	//: concurrent builders agree on one instance.
	actual, _ := layouts.LoadOrStore(t, l)
	//: the cache only ever holds *structLayout.
	if stored, isLayout := actual.(*structLayout); isLayout {
		//: the winner.
		return stored
	}
	//: unreachable: nothing else is stored.
	return l
}

// buildLayout computes the map form of struct type t.
func buildLayout(t reflect.Type, visiting map[reflect.Type]bool) *structLayout {
	visiting[t] = true
	b := &layoutBuilder{
		layout:   &structLayout{byName: make(map[string]*structField, t.NumField())},
		visiting: visiting,
	}
	//: declaration order is encode order.
	for f := range t.Fields() {
		b.addField(f)
	}
	delete(visiting, t)
	//: the finished layout.
	return b.layout
}

// addField places one declared field into the layout.
func (b *layoutBuilder) addField(f reflect.StructField) {
	tag := parseTag(f.Tag.Get(tagKey))
	//: "-" leaves the field out, marker included.
	if tag.name == tagSkip {
		//: nothing to place.
		return
	}
	//: the marker field carries options, never data.
	if f.Name == markerField {
		b.applyMarker(tag)
		//: the marker is unexported, so it is not a field either.
		return
	}
	//: unexported fields are left out unless embedded.
	if !f.IsExported() && !f.Anonymous {
		//: not part of the wire form.
		return
	}
	field := &structField{
		typ:       f.Type,
		name:      cmpOrName(tag.name, f.Name),
		index:     []int{f.Index[0]},
		omitEmpty: b.omitAll || tag.has(optOmitEmpty),
	}
	//: an embedded struct may contribute its own fields instead.
	if f.Anonymous && !tag.has(optNoInline) && b.inline(field, tag.has(optInline)) {
		//: the embedded struct's own name still decodes, as a nested map.
		b.alias(field.name, field)
		return
	}
	b.add(field)
	//: an alias decodes into the same field.
	if alias, ok := tag.opts[optAlias]; ok && alias != "" {
		b.alias(alias, field)
	}
}

// applyMarker reads the struct-wide options of a _msgpack field.
func (b *layoutBuilder) applyMarker(tag tagSpec) {
	b.layout.asArray = tag.has(optAsArray) || tag.has(optAsArrayCamel)
	//: omitempty on the marker reaches every LATER field.
	if tag.has(optOmitEmpty) {
		//: latch it for the rest of the declaration.
		b.omitAll = true
	}
}

// inline places an embedded struct's fields in the layout when the rules
// allow it, and reports whether it did. forced is the inline option: it
// inlines whatever collides and drops the colliding names; otherwise one
// collision keeps the embedded struct as a single field.
func (b *layoutBuilder) inline(field *structField, forced bool) bool {
	et := field.typ
	//: inline through any number of pointers.
	for et.Kind() == reflect.Pointer {
		et = et.Elem()
	}
	//: only a plain struct, not on the current embedding path, inlines.
	if et.Kind() != reflect.Struct || hasCustomCodec(et) || b.visiting[et] {
		//: keep it as one field.
		return false
	}
	sub := buildLayout(et, b.visiting)
	//: a collision keeps the embedded struct whole unless inlining is forced.
	if !forced && b.collides(sub) {
		//: keep it as one field.
		return false
	}
	//: a type refused on its own is refused once inlined, too.
	if sub.dupName != "" {
		b.fail(sub.dupName)
	}
	//: graft each embedded field under the embedding's index.
	for _, sf := range sub.fields {
		//: a forced inline drops the names already taken.
		if _, taken := b.layout.byName[sf.name]; taken {
			continue
		}
		b.add(&structField{
			typ: sf.typ, name: sf.name, omitEmpty: sf.omitEmpty,
			index: append(append(make([]int, 0, len(field.index)+len(sf.index)), field.index...), sf.index...),
		})
	}
	//: inlined.
	return true
}

// collides reports whether any of sub's keys is already taken.
func (b *layoutBuilder) collides(sub *structLayout) bool {
	//: one collision is enough.
	for _, sf := range sub.fields {
		//: an earlier field already owns the key.
		if _, taken := b.layout.byName[sf.name]; taken {
			//: collision.
			return true
		}
	}
	//: no collision.
	return false
}

// add appends a field to the encode order and the decode index.
func (b *layoutBuilder) add(field *structField) {
	//: two fields claiming one key would write a map that repeats it.
	if _, taken := b.layout.byName[field.name]; taken {
		b.fail(field.name)
		return
	}
	field.wireName = appendLength(nil, len(field.name), &strForms)
	field.wireName = append(field.wireName, field.name...)
	b.layout.fields = append(b.layout.fields, field)
	b.layout.byName[field.name] = field
	//: one optional field is enough to take the counting path.
	if field.omitEmpty {
		//: latch it.
		b.layout.hasOmitEmpty = true
	}
}

// alias makes name decode into field, unless a real field already owns it.
func (b *layoutBuilder) alias(name string, field *structField) {
	//: a real field keeps its key.
	if _, taken := b.layout.byName[name]; taken {
		//: the alias loses.
		return
	}
	b.layout.byName[name] = field
}

// fail records the type's first defect.
func (b *layoutBuilder) fail(name string) {
	//: the first defect is the one reported.
	if b.layout.dupName == "" {
		//: remember the key; the failure is built where it is reported.
		b.layout.dupName = name
	}
}

// defect returns the type's defect as an encode or a decode failure, or nil
// when the type has none.
func (l *structLayout) defect(t reflect.Type, encode bool) error {
	//: a sound type.
	if l.dupName == "" {
		//: nothing to report.
		return nil
	}
	//: the direction decides the code; the key is named, never a value.
	if encode {
		//: MARSHAL_FAILED.
		return marshalFault(detailDuplicateKey, typeField(t), errs.String(fieldName, l.dupName))
	}
	//: UNMARSHAL_FAILED.
	return unmarshalFault(detailDuplicateKey, typeField(t), errs.String(fieldName, l.dupName))
}

// hasCustomCodec reports whether t, or a pointer to it, encodes or decodes
// itself, which keeps it from being inlined.
func hasCustomCodec(t reflect.Type) bool {
	//: time.Time is the timestamp extension.
	if t == timeType {
		//: not a plain struct.
		return true
	}
	pt := reflect.PointerTo(t)
	//: any of the self-coding interfaces, on the value or its pointer.
	for _, iface := range customCodecTypes {
		//: value or pointer receiver.
		if t.Implements(iface) || pt.Implements(iface) {
			//: not a plain struct.
			return true
		}
	}
	//: plain.
	return false
}

// parseTag reads `name,opt,key:value`. A first element containing a colon is
// an option, not a name, as the vendor's tag parser read it.
func parseTag(s string) tagSpec {
	var spec tagSpec
	//: an absent tag is the zero spec.
	if s == "" {
		//: Go field name, no options.
		return spec
	}
	head, rest, _ := strings.Cut(s, ",")
	first := strings.TrimSpace(head)
	//: "key:value" in first position is an option.
	if strings.Contains(first, ":") {
		spec.setOption(first)
	} else {
		spec.name = first
	}
	//: the remaining elements are options.
	for opt := range strings.SplitSeq(rest, ",") {
		spec.setOption(strings.TrimSpace(opt))
	}
	//: the parsed tag.
	return spec
}

// setOption records one option, bare or key:value.
func (s *tagSpec) setOption(opt string) {
	//: an empty element (",,") carries nothing.
	if opt == "" {
		//: skip it.
		return
	}
	//: options are allocated only for tags that have any.
	if s.opts == nil {
		s.opts = map[string]string{}
	}
	key, value, _ := strings.Cut(opt, ":")
	s.opts[strings.TrimSpace(key)] = strings.TrimSpace(value)
}

// has reports whether the option is present.
func (s *tagSpec) has(opt string) bool {
	_, ok := s.opts[opt]
	//: presence, whatever its value.
	return ok
}

// cmpOrName returns the tag name when set, else the Go field name.
func cmpOrName(tagName, fieldName string) string {
	//: an empty tag name means the Go name.
	if tagName == "" {
		//: Go field name.
		return fieldName
	}
	//: the tag's name.
	return tagName
}
