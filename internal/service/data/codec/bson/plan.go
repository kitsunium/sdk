package bson

import (
	"encoding"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// planKind is the mapping a plan applies.
type planKind uint8

// The mappings. Exact types are matched before kinds, so a time.Time is a
// datetime and not a struct, and a []byte is a binary and not an array.
const (
	// kindUnsupported has no BSON form: channels, functions, complex numbers,
	// uintptr, unsafe.Pointer.
	kindUnsupported planKind = iota
	// kindBool is a bool.
	kindBool
	// kindInt32 is int8, int16 or int32: always an int32.
	kindInt32
	// kindInt is int: an int32 when the value fits, an int64 otherwise.
	kindInt
	// kindInt64 is int64: an int64, or an int32 under minsize when it fits.
	kindInt64
	// kindUint16 is uint8 or uint16: always an int32.
	kindUint16
	// kindUint64 is uint, uint32 or uint64: an int64, an int32 under minsize
	// when it fits; past the int64 range it is refused.
	kindUint64
	// kindFloat32 is float32, written as a double.
	kindFloat32
	// kindFloat64 is float64.
	kindFloat64
	// kindString is a string kind.
	kindString
	// kindBytes is exactly []byte: a generic binary, null when nil.
	kindBytes
	// kindByteSlice is another slice type whose element type is byte.
	kindByteSlice
	// kindByteArray is an array whose element type is byte.
	kindByteArray
	// kindSlice is any other slice: an array, null when nil.
	kindSlice
	// kindArray is any other array: an array.
	kindArray
	// kindD is exactly D.
	kindD
	// kindDocSlice is another slice type convertible to D: a document.
	kindDocSlice
	// kindDocArray is an array of E: a document.
	kindDocArray
	// kindMap is a map: a document, null when nil.
	kindMap
	// kindStruct is a struct: a document of its fields.
	kindStruct
	// kindPointer is a pointer: its target, null when nil.
	kindPointer
	// kindAny is the empty interface: its dynamic value, null when nil.
	kindAny
	// kindInterface is a non-empty interface: its dynamic value on encode;
	// there is nothing to decode into.
	kindInterface
	// kindTime is time.Time: a UTC datetime, truncated to the millisecond.
	kindTime
	// kindURL is url.URL: its String form.
	kindURL
	// kindJSONNumber is json.Number: an int64 when it parses as one, a double
	// otherwise.
	kindJSONNumber
	// kindObjectID is ObjectID.
	kindObjectID
	// kindDateTime is DateTime.
	kindDateTime
	// kindBinary is Binary.
	kindBinary
	// kindRegex is Regex.
	kindRegex
	// kindDBPointer is DBPointer.
	kindDBPointer
	// kindJavaScript is JavaScript.
	kindJavaScript
	// kindSymbol is Symbol.
	kindSymbol
	// kindCodeWithScope is CodeWithScope.
	kindCodeWithScope
	// kindTimestamp is Timestamp.
	kindTimestamp
	// kindDecimal128 is Decimal128.
	kindDecimal128
	// kindMinKey is MinKey.
	kindMinKey
	// kindMaxKey is MaxKey.
	kindMaxKey
	// kindUndefined is Undefined.
	kindUndefined
	// kindNull is Null.
	kindNull
)

// hookMode says whether, and how, a type's own method replaces the mapping.
type hookMode uint8

// The ways a method is reached.
const (
	// hookNone: the type does not have the method.
	hookNone hookMode = iota
	// hookValue: the type itself has it.
	hookValue
	// hookPointer: only its pointer has it, so it is called on an addressable
	// value and the kind mapping is used on any other.
	hookPointer
)

// keyMode is how a map key becomes an element name, and back.
type keyMode uint8

// The key mappings.
const (
	// keyUnsupported: the key type has no element-name form.
	keyUnsupported keyMode = iota
	// keyString: a string kind, used as is.
	keyString
	// keyText: encoding.TextMarshaler or TextUnmarshaler.
	keyText
	// keyInt: a signed integer kind, in decimal.
	keyInt
	// keyUint: an unsigned integer kind, in decimal.
	keyUint
)

// typePlan is everything the codec decided about one Go type.
type typePlan struct {
	// typ is the type planned.
	typ reflect.Type
	// kind is the mapping.
	kind planKind
	// elem plans a pointer's target, a slice's or array's element, a map's
	// value.
	elem *typePlan
	// marshaler says how MarshalBSON is reached, if it is.
	marshaler hookMode
	// unmarshaler says how UnmarshalBSON is reached, if it is.
	unmarshaler hookMode
	// zeroer reports that the type has IsZero, which omitempty then calls.
	zeroer bool
	// stringAnyMap reports a map whose underlying type is map[string]any,
	// decoded without reflection.
	stringAnyMap bool
	// keyEncode and keyDecode map a map's keys; they differ when only one
	// direction has a text method.
	keyEncode, keyDecode keyMode
	// slots recycles a map's key and value slots: iterating or filling a map
	// through reflection needs two settable values, and allocating them per
	// call would cost two allocations per map, nested maps included.
	slots *recycler.Pool[*mapSlots]
	// fields are a struct's fields in encoding order.
	fields []*fieldPlan
	// byName indexes fields by element name.
	byName map[string]*fieldPlan
	// inlineMap is the field holding a struct's ",inline" map, or nil.
	inlineMap *fieldPlan
	// invalid says why the type cannot be encoded or decoded at all (a
	// duplicated element name, a malformed ",inline"); each direction raises it
	// under its own code when the type is used.
	invalid string
}

// fieldPlan is one struct field, as an element.
type fieldPlan struct {
	// name is the element name.
	name string
	// goName is the Go field name, for error messages.
	goName string
	// index is the path to the field, longer than one when it comes from an
	// ",inline" struct.
	index []int
	// position is the field's place in its struct plan's fields, so a decode
	// can guess that the next element names the next field.
	position int
	// plan is the field type's plan.
	plan *typePlan
	// nameBytes is name as bytes, for comparing with an element name read
	// from the input without converting either.
	nameBytes []byte
	// flags are the tag's options, and whether the name is writable.
	flags fieldFlags
}

// fieldFlags are a field's options, one bit each.
type fieldFlags uint8

// The options a bson tag sets, and the one property of a name the plan checks.
const (
	// flagOmitEmpty leaves the field out when it is empty.
	flagOmitEmpty fieldFlags = 1 << iota
	// flagMinSize writes an integer that fits as an int32.
	flagMinSize
	// flagTruncate lets a decode drop a double's fraction into an integer.
	flagTruncate
	// flagInline flattens a struct's fields, or collects unclaimed elements
	// into a map.
	flagInline
	// flagBadName marks a name holding a NUL or invalid UTF-8, which an
	// element name cannot be; encoding the field fails.
	flagBadName
)

// has reports whether every bit of flag is set.
func (f fieldFlags) has(flag fieldFlags) bool {
	//: all of them.
	return f&flag == flag
}

// The types matched exactly, before their kind is looked at.
var (
	// typeOfTime is time.Time.
	typeOfTime = reflect.TypeFor[time.Time]()
	// typeOfBytes is []byte.
	typeOfBytes = reflect.TypeFor[[]byte]()
	// typeOfByte is byte.
	typeOfByte = reflect.TypeFor[byte]()
	// typeOfURL is url.URL.
	typeOfURL = reflect.TypeFor[url.URL]()
	// typeOfJSONNumber is json.Number.
	typeOfJSONNumber = reflect.TypeFor[json.Number]()
	// typeOfD is D.
	typeOfD = reflect.TypeFor[D]()
	// typeOfE is E.
	typeOfE = reflect.TypeFor[E]()
	// typeOfM is M.
	typeOfM = reflect.TypeFor[M]()
	// typeOfString is string.
	typeOfString = reflect.TypeFor[string]()
	// typeOfStringAnyMap is map[string]any.
	typeOfStringAnyMap = reflect.TypeFor[map[string]any]()
	// typeOfAny is the empty interface.
	typeOfAny = reflect.TypeFor[any]()
	// exactKinds maps the codec's own value types, and the stdlib types it
	// knows, to their mapping.
	exactKinds = map[reflect.Type]planKind{
		typeOfTime:                       kindTime,
		typeOfBytes:                      kindBytes,
		typeOfURL:                        kindURL,
		typeOfJSONNumber:                 kindJSONNumber,
		typeOfD:                          kindD,
		reflect.TypeFor[ObjectID]():      kindObjectID,
		reflect.TypeFor[DateTime]():      kindDateTime,
		reflect.TypeFor[Binary]():        kindBinary,
		reflect.TypeFor[Regex]():         kindRegex,
		reflect.TypeFor[DBPointer]():     kindDBPointer,
		reflect.TypeFor[JavaScript]():    kindJavaScript,
		reflect.TypeFor[Symbol]():        kindSymbol,
		reflect.TypeFor[CodeWithScope](): kindCodeWithScope,
		reflect.TypeFor[Timestamp]():     kindTimestamp,
		reflect.TypeFor[Decimal128]():    kindDecimal128,
		reflect.TypeFor[MinKey]():        kindMinKey,
		reflect.TypeFor[MaxKey]():        kindMaxKey,
		reflect.TypeFor[Undefined]():     kindUndefined,
		reflect.TypeFor[Null]():          kindNull,
	}
	// typeOfMarshaler is the method set a type implements to write its own
	// document.
	typeOfMarshaler = reflect.TypeFor[interface{ MarshalBSON() ([]byte, error) }]()
	// typeOfUnmarshaler is the method set a type implements to read its own
	// value.
	typeOfUnmarshaler = reflect.TypeFor[interface{ UnmarshalBSON(data []byte) error }]()
	// typeOfZeroer is the method set omitempty asks.
	typeOfZeroer = reflect.TypeFor[interface{ IsZero() bool }]()
	// typeOfTextMarshaler is encoding.TextMarshaler, for map keys.
	typeOfTextMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
	// typeOfTextUnmarshaler is encoding.TextUnmarshaler, for map keys.
	typeOfTextUnmarshaler = reflect.TypeFor[encoding.TextUnmarshaler]()
	// scalarKinds maps the scalar reflect.Kinds to their mapping.
	scalarKinds = map[reflect.Kind]planKind{
		reflect.Bool:    kindBool,
		reflect.Int8:    kindInt32,
		reflect.Int16:   kindInt32,
		reflect.Int32:   kindInt32,
		reflect.Int:     kindInt,
		reflect.Int64:   kindInt64,
		reflect.Uint8:   kindUint16,
		reflect.Uint16:  kindUint16,
		reflect.Uint:    kindUint64,
		reflect.Uint32:  kindUint64,
		reflect.Uint64:  kindUint64,
		reflect.Float32: kindFloat32,
		reflect.Float64: kindFloat64,
		reflect.String:  kindString,
	}
	// integerKeyModes maps the integer reflect.Kinds to their key mode;
	// uintptr is included, as the previous library included it.
	integerKeyModes = map[reflect.Kind]keyMode{
		reflect.Int:     keyInt,
		reflect.Int8:    keyInt,
		reflect.Int16:   keyInt,
		reflect.Int32:   keyInt,
		reflect.Int64:   keyInt,
		reflect.Uint:    keyUint,
		reflect.Uint8:   keyUint,
		reflect.Uint16:  keyUint,
		reflect.Uint32:  keyUint,
		reflect.Uint64:  keyUint,
		reflect.Uintptr: keyUint,
	}
	// tagOptions maps each option word to its flag.
	tagOptions = map[string]fieldFlags{
		"omitempty": flagOmitEmpty,
		"minsize":   flagMinSize,
		"truncate":  flagTruncate,
		"inline":    flagInline,
	}
	// plans maps reflect.Type to its complete *typePlan.
	plans sync.Map
	// planMu serialises construction; reading plans takes no lock.
	planMu sync.Mutex
)

// planFor returns the plan of t, building and publishing it — and every plan
// it reaches — on first use.
func planFor(t reflect.Type) *typePlan {
	//: the lock-free hit.
	if p, ok := publishedPlan(t); ok {
		//: published.
		return p
	}
	planMu.Lock()
	defer planMu.Unlock()
	//: another goroutine may have built it while this one waited.
	if p, ok := publishedPlan(t); ok {
		//: published meanwhile.
		return p
	}
	builder := planBuilder{pending: map[reflect.Type]*typePlan{}}
	built := builder.plan(t)
	//: publish every plan built along the way, each complete by now.
	for typ, p := range builder.pending {
		plans.Store(typ, p)
	}
	//: the plan asked for.
	return built
}

// publishedPlan returns t's published plan, if there is one.
func publishedPlan(t reflect.Type) (*typePlan, bool) {
	cached, ok := plans.Load(t)
	//: not built yet.
	if !ok {
		//: none.
		return nil, false
	}
	p, ok := cached.(*typePlan)
	//: published plans are only ever *typePlan.
	return p, ok
}

// planBuilder builds plans for one planFor call, under planMu.
type planBuilder struct {
	// pending holds the plans built so far, so a recursive type reaches its
	// own plan instead of recursing forever.
	pending map[reflect.Type]*typePlan
}

// plan returns the plan of t: a published one, one built earlier in this
// call (perhaps still being filled), or a new one.
func (b *planBuilder) plan(t reflect.Type) *typePlan {
	//: published already.
	if p, ok := publishedPlan(t); ok {
		//: published.
		return p
	}
	//: built, or being built, in this call.
	if p, ok := b.pending[t]; ok {
		//: the same pointer, filled by the time anything reads it.
		return p
	}
	p := &typePlan{typ: t}
	b.pending[t] = p
	b.fill(p)
	//: complete.
	return p
}

// fill decides the mapping of p.typ.
func (b *planBuilder) fill(p *typePlan) {
	t := p.typ
	p.zeroer = t.Implements(typeOfZeroer)
	//: an exact type takes precedence over the type's methods and kind.
	if kind, ok := exactKinds[t]; ok {
		p.kind = kind
		return
	}
	p.marshaler = hookFor(t, typeOfMarshaler)
	p.unmarshaler = hookFor(t, typeOfUnmarshaler)
	//: the kind decides the rest.
	b.fillKind(p)
}

// hookFor reports how t reaches the methods of iface.
func hookFor(t, iface reflect.Type) hookMode {
	//: the type has them.
	if t.Implements(iface) {
		//: on every value.
		return hookValue
	}
	//: only its pointer does.
	if t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(iface) {
		//: on an addressable value.
		return hookPointer
	}
	//: neither.
	return hookNone
}

// fillKind decides the mapping of a type that is not matched exactly.
func (b *planBuilder) fillKind(p *typePlan) {
	t := p.typ
	//: the scalars, by table.
	if kind, ok := scalarKinds[t.Kind()]; ok {
		p.kind = kind
		return
	}
	//: one mapping per composite kind.
	switch t.Kind() {
	case reflect.Slice:
		b.fillSlice(p)
	case reflect.Array:
		b.fillArray(p)
	case reflect.Map:
		b.fillMap(p)
	case reflect.Struct:
		p.kind = kindStruct
		b.fillStruct(p)
	case reflect.Pointer:
		p.kind = kindPointer
		p.elem = b.plan(t.Elem())
	case reflect.Interface:
		p.kind = interfaceKind(t)
	default:
		//: channels, functions, complex numbers, uintptr, unsafe.Pointer.
		p.kind = kindUnsupported
	}
}

// interfaceKind maps an interface type: the empty interface decodes, any
// other has no concrete type for a decode to make.
func interfaceKind(t reflect.Type) planKind {
	//: no methods: the empty interface.
	if t.NumMethod() == 0 {
		//: any.
		return kindAny
	}
	//: a non-empty interface.
	return kindInterface
}

// fillSlice decides a slice's mapping: bytes, a document of E, or an array.
func (b *planBuilder) fillSlice(p *typePlan) {
	t := p.typ
	p.elem = b.plan(t.Elem())
	//: a byte element makes a binary.
	if t.Elem() == typeOfByte {
		p.kind = kindByteSlice
		return
	}
	//: a slice of E is an ordered document.
	if t.ConvertibleTo(typeOfD) {
		p.kind = kindDocSlice
		return
	}
	p.kind = kindSlice
}

// fillArray decides an array's mapping, as fillSlice does.
func (b *planBuilder) fillArray(p *typePlan) {
	t := p.typ
	p.elem = b.plan(t.Elem())
	//: one mapping per element type.
	switch t.Elem() {
	//: a byte element makes a binary.
	case typeOfByte:
		p.kind = kindByteArray
	//: an E element makes a document.
	case typeOfE:
		p.kind = kindDocArray
	default:
		p.kind = kindArray
	}
}

// fillMap decides a map's mapping and how its keys become element names.
func (b *planBuilder) fillMap(p *typePlan) {
	t := p.typ
	p.kind = kindMap
	p.elem = b.plan(t.Elem())
	p.stringAnyMap = t.Key() == typeOfString && t.Elem() == typeOfAny
	p.keyEncode, p.keyDecode = keyModes(t.Key())
	p.slots = recycler.NewPool(func() *mapSlots {
		//: one settable key and one settable value of the map's types.
		return &mapSlots{key: reflect.New(t.Key()).Elem(), value: reflect.New(t.Elem()).Elem()}
	})
}

// mapSlots are the settable key and value a reflective map walk reuses.
type mapSlots struct {
	// key holds the current entry's key.
	key reflect.Value
	// value holds the current entry's value.
	value reflect.Value
}

// acquireSlots borrows p's slots; release them with releaseSlots.
func (p *typePlan) acquireSlots() *mapSlots {
	//: from the map type's own pool.
	return p.slots.Get()
}

// releaseSlots zeroes the slots, so a pooled slot pins nothing the caller's
// map held, and returns them.
func (p *typePlan) releaseSlots(s *mapSlots) {
	s.key.SetZero()
	s.value.SetZero()
	p.slots.Put(s)
}

// keyModes decides how a map key type is written and read. A string kind is
// written as itself even when it has MarshalText, while UnmarshalText, when
// present, is preferred for reading: the asymmetry is the previous library's,
// kept so the same map keys round-trip as they did.
func keyModes(k reflect.Type) (encode, decode keyMode) {
	encode, decode = keyUnsupported, keyUnsupported
	//: writing.
	switch {
	//: a string kind first.
	case k.Kind() == reflect.String:
		encode = keyString
	//: then a text method.
	case k.Implements(typeOfTextMarshaler):
		encode = keyText
	default:
		encode = integerKeyMode(k.Kind())
	}
	//: reading: a text method first.
	if reflect.PointerTo(k).Implements(typeOfTextUnmarshaler) {
		decode = keyText
	} else if k.Kind() == reflect.String {
		decode = keyString
	} else {
		decode = integerKeyMode(k.Kind())
	}
	//: both directions.
	return encode, decode
}

// integerKeyMode maps an integer kind to its decimal key mode, and anything
// else to keyUnsupported.
func integerKeyMode(k reflect.Kind) keyMode {
	//: the table's mode, or the zero mode for a kind it does not list.
	return integerKeyModes[k]
}

// fillStruct describes a struct's fields: names, options, ",inline" fields
// flattened, a duplicated name refused unless one occurrence is shallower.
func (b *planBuilder) fillStruct(p *typePlan) {
	fields, inlineMap, invalid := b.describeStruct(p.typ, map[reflect.Type]bool{})
	//: a malformed ",inline", or two fields claiming one name.
	if invalid != "" {
		p.invalid = invalid
		return
	}
	p.fields, p.inlineMap = make([]*fieldPlan, 0, len(fields)), inlineMap
	p.byName = make(map[string]*fieldPlan, len(fields))
	//: the decode index; each field is this struct's own copy, its position
	//: in this struct's order.
	for i, f := range fields {
		copied := *f
		copied.position = i
		p.fields = append(p.fields, &copied)
		p.byName[copied.name] = &copied
	}
}

// describeStruct lists t's encoded fields, ",inline" structs expanded in
// place, duplicated names resolved, and t's own ",inline" map. chain holds the
// structs whose ",inline" expansion encloses this one, so a cycle through
// ",inline" is refused; a field that merely refers back to a type being
// planned is an ordinary recursive type and is not one. It reports why t is
// unusable, or "".
func (b *planBuilder) describeStruct(t reflect.Type, chain map[reflect.Type]bool) ([]*fieldPlan, *fieldPlan, string) {
	chain[t] = true
	defer delete(chain, t)
	candidates := make([]*fieldPlan, 0, t.NumField())
	var inlineMap *fieldPlan
	//: in declaration order.
	for i := range t.NumField() {
		sf := t.Field(i)
		tags, skip := parseFieldTags(sf)
		//: unexported fields, embedded ones included, and bson:"-".
		if !sf.IsExported() || skip {
			continue
		}
		//: an ordinary field.
		if !tags.flags.has(flagInline) {
			candidates = append(candidates, b.newField(sf, tags, []int{i}))
			continue
		}
		//: an inline map collects the elements no field claims.
		if sf.Type.Kind() == reflect.Map {
			//: at most one, keyed by string.
			if invalid := inlineMapProblem(t, sf.Type, inlineMap); invalid != "" {
				//: refused.
				return nil, nil, invalid
			}
			inlineMap = b.newField(sf, tags, []int{i})
			continue
		}
		inlined, invalid := b.inlineStruct(t, sf.Type, i, chain)
		//: a malformed ",inline".
		if invalid != "" {
			//: the whole type is refused.
			return nil, nil, invalid
		}
		candidates = append(candidates, inlined...)
	}
	kept, invalid := dominantFields(t, candidates)
	//: the fields, the inline map, and any refusal.
	return kept, inlineMap, invalid
}

// newField builds the plan of one field.
func (b *planBuilder) newField(sf reflect.StructField, tags fieldTags, index []int) *fieldPlan {
	flags := tags.flags
	//: a name an element cannot carry is refused when the field is written.
	if strings.IndexByte(tags.name, 0) >= 0 || !utf8.ValidString(tags.name) {
		flags |= flagBadName
	}
	//: the options parsed from the tag, and the field type's plan.
	return &fieldPlan{
		name:      tags.name,
		nameBytes: []byte(tags.name),
		goName:    sf.Name,
		index:     index,
		plan:      b.plan(sf.Type),
		flags:     flags,
	}
}

// inlineMapProblem says why a map cannot be owner's ",inline" map, or "".
func inlineMapProblem(owner, mapType reflect.Type, existing *fieldPlan) string {
	//: a second one has nowhere to go.
	if existing != nil {
		//: refused.
		return "struct " + owner.String() + " has more than one inline map"
	}
	//: the element name is the key, so the key must be one.
	if mapType.Key() != typeOfString {
		//: refused.
		return "struct " + owner.String() + " has an inline map whose key type is not string"
	}
	//: acceptable.
	return ""
}

// inlineStruct expands an ",inline" struct, or pointer to one, into its
// fields, each path prefixed with the inlining field's index. The inlined
// struct's own ",inline" map is not carried up, as the previous library did
// not carry it.
func (b *planBuilder) inlineStruct(owner, ft reflect.Type, i int, chain map[reflect.Type]bool) ([]*fieldPlan, string) {
	//: a pointer to a struct inlines the struct.
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	//: only a struct has fields to inline.
	if ft.Kind() != reflect.Struct {
		//: refused.
		return nil, "struct " + owner.String() + " has an inline field that is not a struct, a struct pointer or a map"
	}
	//: a struct inlining itself, directly or not, has no finite field list.
	if chain[ft] {
		//: refused.
		return nil, "struct " + owner.String() + " inlines " + ft.String() + " recursively"
	}
	inner, _, invalid := b.describeStruct(ft, chain)
	//: the inlined struct's refusal is this one's.
	if invalid != "" {
		//: refused.
		return nil, invalid
	}
	out := make([]*fieldPlan, 0, len(inner))
	//: each inner field, its path prefixed with this field's index.
	for _, f := range inner {
		copied := *f
		copied.index = append([]int{i}, f.index...)
		out = append(out, &copied)
	}
	//: the flattened fields.
	return out, ""
}

// dominantFields resolves duplicated element names: the shallowest field wins,
// two at the same depth are refused. The survivors come back in declaration
// order, an inlined struct's fields at the position of the field inlining it.
// It reports why the struct is ambiguous, or "".
func dominantFields(t reflect.Type, candidates []*fieldPlan) ([]*fieldPlan, string) {
	//: by name, then depth, then position.
	slices.SortStableFunc(candidates, func(x, y *fieldPlan) int {
		//: name first.
		if c := strings.Compare(x.name, y.name); c != 0 {
			return c
		}
		//: then depth: the shallower occurrence first.
		if len(x.index) != len(y.index) {
			return len(x.index) - len(y.index)
		}
		//: then position.
		return slices.Compare(x.index, y.index)
	})
	kept := make([]*fieldPlan, 0, len(candidates))
	//: one survivor per name.
	for i := 0; i < len(candidates); {
		j := i + 1
		//: the run of candidates sharing the name.
		for j < len(candidates) && candidates[j].name == candidates[i].name {
			j++
		}
		//: two at the shallowest depth: neither dominates.
		if j-i > 1 && len(candidates[i].index) == len(candidates[i+1].index) {
			//: refused, as an ambiguous struct.
			return nil, "struct " + t.String() + " has two fields named " + candidates[i].name + " at the same depth"
		}
		kept = append(kept, candidates[i])
		i = j
	}
	//: back to declaration order.
	slices.SortFunc(kept, func(x, y *fieldPlan) int {
		//: lexicographic on the index path.
		return slices.Compare(x.index, y.index)
	})
	//: the survivors.
	return kept, ""
}

// fieldTags are the options of a bson struct tag.
type fieldTags struct {
	// name is the element name.
	name string
	// flags are the options set.
	flags fieldFlags
}

// parseFieldTags reads the bson tag of sf. The element name defaults to the
// field name in lower case. A struct tag with no key and no colon at all is
// read as a bson tag, as the previous library read it. The options are
// matched in every comma-separated part, the first included: `bson:"inline"`
// both names the element "inline" and sets the option.
func parseFieldTags(sf reflect.StructField) (fieldTags, bool) {
	tags := fieldTags{name: strings.ToLower(sf.Name)}
	tag, ok := sf.Tag.Lookup("bson")
	//: the bare-tag form.
	if !ok && len(sf.Tag) > 0 && !strings.Contains(string(sf.Tag), ":") {
		tag = string(sf.Tag)
	}
	//: the field is skipped.
	if tag == "-" {
		//: nothing else to read.
		return tags, true
	}
	first, rest, more := strings.Cut(tag, ",")
	//: a non-empty first part names the element.
	if first != "" {
		tags.name = first
	}
	//: the first part, too, may be an option.
	tags.flags |= tagOptions[first]
	//: then every part after a comma.
	for more {
		var option string
		option, rest, more = strings.Cut(rest, ",")
		tags.flags |= tagOptions[option]
	}
	//: the field is encoded.
	return tags, false
}

// lookupField finds the field an element names: exactly, then by the name
// lower-cased, as the previous library matched.
func (p *typePlan) lookupField(key []byte) *fieldPlan {
	//: the exact name; the conversion does not allocate in a map index.
	if f, ok := p.byName[string(key)]; ok {
		//: found.
		return f
	}
	//: a name that is its own lower case has nothing more to try.
	if !needsFolding(key) {
		//: no field.
		return nil
	}
	//: the lower-cased name.
	return p.foldedField(key)
}

// foldedField looks up key lower-cased, Unicode folding included.
func (p *typePlan) foldedField(key []byte) *fieldPlan {
	//: one allocation, on the rare path where the exact name missed.
	return p.byName[strings.ToLower(string(key))]
}

// needsFolding reports whether lower-casing key could change it: an ASCII
// upper-case letter, or any non-ASCII byte.
func needsFolding(key []byte) bool {
	//: byte by byte.
	for _, c := range key {
		//: either needs strings.ToLower.
		if c >= utf8.RuneSelf || ('A' <= c && c <= 'Z') {
			//: fold.
			return true
		}
	}
	//: already lower case.
	return false
}
