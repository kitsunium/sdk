package jsonshape

import (
	"cmp"
	"reflect"
	"slices"
	"strings"
)

// maxEmptiableDepth bounds the structs emptiableV2 looks into: a struct
// nested deeper is taken as able to be empty, which only widens Optional.
const maxEmptiableDepth int = 32

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

// optional is member.optional under the walker's semantics: json/v2's own
// omitempty omits a member whose value encodes as null, "", {} or [] —
// never a number or a boolean, and a struct when its fields all can be left
// out (OfV2, #893).
func (w *walker) optional(m *member, root reflect.Type) bool {
	//: encoding/json's definition.
	if !w.v2 {
		//: the v1 rule.
		return m.optional(root)
	}
	//: dropped when zero, or when its encoding can be empty.
	if m.opts.omitZero || (m.opts.omitEmpty && emptiableV2(m.typ, 0)) {
		//: optional.
		return true
	}
	//: dropped when an embedding on the way is a nil pointer.
	return throughPointer(root, m.index)
}

// quoted is member.quoted under the walker's semantics: json/v2's string
// option quotes a number — an integer, a float, json.Number — and nothing
// else.
func (w *walker) quoted(m *member, addressable bool) bool {
	//: encoding/json's definition.
	if !w.v2 {
		//: the v1 rule.
		return m.quoted(addressable)
	}
	//: the option is absent, or the target is not a number.
	return m.opts.quoted && quotableV2(m.typ)
}

// quotableV2 reports whether json/v2's string option applies to t: a number
// that writes nothing of its own, through one unnamed pointer, or
// json.Number.
func quotableV2(t reflect.Type) bool {
	//: one unnamed pointer is followed.
	if t.Name() == "" && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	//: json.Number honours the option itself.
	if t == numberType {
		//: quoted.
		return true
	}
	//: a type writing itself ignores the option.
	if writesJSON(t, true) || writesText(t, true) {
		//: not quoted.
		return false
	}
	//: the numeric kinds.
	switch t.Kind() {
	//: integers and floats.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		//: quoted.
		return true
	//: anything else: json/v2 refuses the option on it.
	default:
		//: not quoted.
		return false
	}
}

// refusesQuoting reports whether json/v2 refuses a value of t carrying the
// string option: anything that is not a number and writes nothing of its own.
func refusesQuoting(t reflect.Type) bool {
	target := t
	//: one unnamed pointer is followed.
	if target.Name() == "" && target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	//: a type writing itself ignores the option, a number takes it.
	return !quotableV2(t) && !writesJSON(target, true) && !writesText(target, true)
}

// emptiableV2 reports whether a value of t can encode, under json/v2's own
// options, as null, "", {} or []: a pointer, an interface, a string, a slice,
// a map, a zero-length array, a type writing its own JSON or text — except
// time.Time, whose text is never empty —, or a struct whose fields can all be
// left out.
func emptiableV2(t reflect.Type, depth int) bool {
	//: the types whose encoding the kind does not tell: a date-time is never
	//: empty, a duration is refused, an empty json.Number is written as 0.
	if t == timeType || t == durationType || t == numberType {
		//: never empty.
		return false
	}
	//: whatever a type writing itself writes may be empty.
	if writesJSON(t, true) || writesText(t, true) {
		//: may be empty.
		return true
	}
	//: by kind.
	return kindEmptiableV2(t, depth)
}

// kindEmptiableV2 is emptiableV2 for a type laid out by its kind.
func kindEmptiableV2(t reflect.Type, depth int) bool {
	//: by kind.
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.String, reflect.Slice, reflect.Map:
		//: null, "", [] or {}.
		return true
	case reflect.Array:
		//: [] or "" only when it holds nothing.
		return t.Len() == 0
	case reflect.Struct:
		//: {} when every field can be left out.
		return depth >= maxEmptiableDepth || structEmptiableV2(t, depth)
	default:
		//: a number or a boolean is never empty.
		return false
	}
}

// structEmptiableV2 reports whether every member of the struct t can be left
// out under json/v2's own options, so its value can encode as {}.
func structEmptiableV2(t reflect.Type, depth int) bool {
	written, _ := members(t)
	//: each member.
	for _, m := range written {
		//: one member always written keeps the object non-empty.
		leftOut := m.opts.omitZero || (m.opts.omitEmpty && emptiableV2(m.typ, depth+1)) || throughPointer(t, m.index)
		//: one member that cannot go.
		if !leftOut {
			//: never {}.
			return false
		}
	}
	//: every member can go.
	return true
}

// structRefusedV2 reports whether json/v2's own options refuse the struct t
// whole, where encoding/json's legacy options ignore the fault: a json tag
// whose unquoted name runs into a quote or a backslash, or that ends with a
// comma; an embedded field with no name of its own carrying options other
// than embed; or an exported embedded field of a type that is not a struct.
// The structs it inlines are looked into, once each.
func structRefusedV2(t reflect.Type, seen map[reflect.Type]bool) bool {
	seen[t] = true
	//: each declared field.
	for field := range t.Fields() {
		//: an unexported field that is not embedded is never looked at.
		if !field.IsExported() && !field.Anonymous {
			continue
		}
		//: a tag json/v2 cannot parse.
		if tagRefusedV2(field.Tag.Get("json")) {
			//: refused.
			return true
		}
		//: an embedding json/v2 inlines, or refuses.
		if field.Anonymous && embeddingRefusedV2(field, seen) {
			//: refused.
			return true
		}
	}
	//: accepted.
	return false
}

// embeddingRefusedV2 reports whether json/v2 refuses the embedded field: one
// with no name of its own is inlined, so it may carry no option but embed and
// must be a struct — an exported non-struct needs a name.
func embeddingRefusedV2(field reflect.StructField, seen map[reflect.Type]bool) bool {
	opts, ignored := parseTag(field)
	//: `json:"-"`, or a named member: not inlined.
	if ignored || opts.hasName {
		//: judged as a member.
		return false
	}
	//: options an inlined field cannot have.
	if opts.hasOthers() {
		//: refused.
		return true
	}
	inner := indirect(field.Type)
	//: a non-struct cannot be inlined.
	if inner.Kind() != reflect.Struct {
		//: refused when json/v2 would have to write it.
		return field.IsExported()
	}
	//: the inlined struct's own fields, once.
	return !seen[inner] && structRefusedV2(inner, seen)
}

// tagRefusedV2 reports whether json/v2 refuses the json tag: an unquoted
// name followed by a reserved character other than a comma, or a trailing
// comma.
func tagRefusedV2(tag string) bool {
	//: no tag, or the "never" spelling.
	if tag == "" || tag == "-" {
		//: accepted.
		return false
	}
	//: a trailing comma.
	if strings.HasSuffix(tag, ",") {
		//: refused.
		return true
	}
	//: a quoted name is read by the shared parser.
	if strings.HasPrefix(tag, "'") {
		//: accepted here.
		return false
	}
	end := strings.IndexAny(tag, reservedInName)
	//: the name runs into a reserved character that is not the comma.
	return end >= 0 && tag[end] != ','
}
