package jsonshape

import (
	"encoding/json"
	"reflect"
	"time"
)

// The types described by what they write rather than by their kind.
var (
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
	numberType   = reflect.TypeFor[json.Number]()
)

// walker describes one type. path holds the named types being described on
// the way down, so a type that reaches itself is described once and then
// referenced. v2 describes what encoding/json/v2 writes under its own
// default options instead of encoding/json's (OfV2, #893).
type walker struct {
	path map[reflect.Type]bool
	v2   bool
}

// newWalker returns a walker with nothing on its path.
func newWalker() *walker {
	//: an empty path.
	return &walker{path: map[reflect.Type]bool{}}
}

// shape describes t, for a value that is addressable where encoding/json
// meets it or not: only an addressable value has its pointer methods called.
func (w *walker) shape(t reflect.Type, addressable bool) *ShapeValue {
	//: a nil interface's type.
	if t == nil {
		return &ShapeValue{Kind: Any, Nullable: true}
	}
	//: a type already being described further up: a reference, not a loop.
	if w.path[t] {
		ref := named(t, refKind(t))
		ref.Ref = ref.Name
		return ref
	}
	//: only a named type can reach itself; only it goes on the path.
	if t.Name() != "" {
		w.path[t] = true
		defer delete(w.path, t)
	}
	//: a pointer writes its element, which it makes addressable, or null.
	if t.Kind() == reflect.Pointer {
		element := w.shape(t.Elem(), true)
		element.Nullable = true
		return element
	}
	//: a type described by what it writes.
	if known, isKnown := w.knownShape(t); isKnown {
		return known
	}
	//: json/v2's own options call a pointer method on every value: the
	//: encoder makes a copy addressable when it must.
	methodsReach := addressable || w.v2
	//: a type that writes itself: the kind is its own business.
	if t.Kind() != reflect.Interface && writesJSON(t, methodsReach) {
		return named(t, Any)
	}
	//: a type that writes itself as text: a string.
	if t.Kind() != reflect.Interface && writesText(t, methodsReach) {
		return named(t, String)
	}
	//: laid out by reflection.
	return w.byKind(t, addressable)
}

// byKind describes a type encoding/json lays out from its kind: the
// composite kinds here, the scalars in scalar.
//
// Where a container puts its elements decides their addressability, as in
// json/v2: a slice's and a pointer's elements are addressable, an array's and
// a struct's inherit it, a map's values never are.
func (w *walker) byKind(t reflect.Type, addressable bool) *ShapeValue {
	shape := named(t, Any)
	//: the containers here, the scalars in scalar.
	switch t.Kind() {
	//: bytes are base64 text; other elements an array; nil is null.
	case reflect.Slice:
		w.slice(shape, t)
	//: always an array — even of bytes — never null.
	case reflect.Array:
		w.array(shape, t, addressable)
	//: an object keyed by the map's keys, or refused for a key it cannot write.
	case reflect.Map:
		w.mapOf(shape, t)
	//: an object of the struct's members.
	case reflect.Struct:
		w.object(shape, t, addressable)
	//: everything that is not a container.
	default:
		scalar(shape, t)
	}
	return shape
}

// scalar fills shape for a kind that holds no other value.
func scalar(shape *ShapeValue, t reflect.Type) {
	//: by kind.
	switch t.Kind() {
	//: true or false.
	case reflect.Bool:
		shape.Kind = Boolean
	//: an integer, refined by its Go kind.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		shape.Kind, shape.Format = Integer, t.Kind().String()
	//: a float, refined by its Go kind.
	case reflect.Float32, reflect.Float64:
		shape.Kind, shape.Format = Number, t.Kind().String()
	//: text.
	case reflect.String:
		shape.Kind = String
	//: anything at all, or null.
	case reflect.Interface:
		shape.Kind, shape.Nullable = Any, true
	//: channels, functions, complex numbers, unsafe pointers.
	default:
		shape.Kind = Unsupported
	}
}

// slice fills shape for a slice: a base64 string for bytes that write nothing
// of their own, an array otherwise; either may be null.
func (w *walker) slice(shape *ShapeValue, t reflect.Type) {
	//: json/v2 writes a nil slice as [] — a nil []byte as "".
	shape.Nullable = !w.v2
	element := t.Elem()
	//: bytes — named or not — unless each byte encodes itself.
	if element.Kind() == reflect.Uint8 && !hasAnyMethod(element) {
		shape.Kind, shape.Format = String, "base64"
		return
	}
	shape.Kind, shape.Items = Array, w.shape(element, true)
}

// array describes an array: its elements, or — under json/v2, which writes
// a byte array as a byte slice is written — a base64 string.
func (w *walker) array(shape *ShapeValue, t reflect.Type, addressable bool) {
	//: [N]byte under json/v2.
	if w.v2 && t.Elem().Kind() == reflect.Uint8 && !hasAnyMethod(t.Elem()) {
		shape.Kind, shape.Format = String, "base64"
		//: a string.
		return
	}
	shape.Kind, shape.Items = Array, w.shape(t.Elem(), addressable)
}

// mapOf fills shape for a map: an object whose values are the map's, or
// Unsupported when encoding/json cannot write the key as a member name.
func (w *walker) mapOf(shape *ShapeValue, t reflect.Type) {
	//: a key encoding/json refuses refuses the whole map.
	if !keyWritable(t.Key()) {
		shape.Kind = Unsupported
		return
	}
	//: json/v2 writes a nil map as {}.
	shape.Kind, shape.Nullable, shape.Values = Map, !w.v2, w.shape(t.Elem(), false)
}

// object fills shape for a struct with its members and, when an embedded map
// collects extra members, their value shape.
func (w *walker) object(shape *ShapeValue, t reflect.Type, addressable bool) {
	//: json/v2 refuses a struct whose fields its own options cannot read.
	if w.v2 && structRefusedV2(t, map[reflect.Type]bool{}) {
		shape.Kind = Unsupported
		//: refused whole.
		return
	}
	shape.Kind = Object
	written, extra := members(t)
	//: in the order encoding/json writes them.
	for _, member := range written {
		//: a field inherits the struct's addressability, unless an embedded
		//: pointer on its path makes it addressable.
		fieldAddressable := addressable || throughPointer(t, member.index)
		memberShape := w.shape(member.typ, fieldAddressable)
		//: json/v2 refuses the string option on what is not a number.
		if w.v2 && member.opts.quoted && refusesQuoting(member.typ) {
			memberShape = named(member.typ, Unsupported)
		}
		shape.Fields = append(shape.Fields, FieldValue{
			Name:     member.name,
			Shape:    memberShape,
			Optional: w.optional(member, t),
			Quoted:   w.quoted(member, fieldAddressable),
			Rules:    member.tag.Get("validate"),
			GoName:   member.goName,
			Tag:      member.tag,
			Index:    member.index,
		})
	}
	//: an embedded map or jsontext.Value takes the members no field names.
	if extra != nil {
		shape.Values = w.extraValues(extra)
	}
}

// extraValues describes the members an embedded fallback collects: a map's
// values, or anything for a jsontext.Value.
func (w *walker) extraValues(extra reflect.Type) *ShapeValue {
	//: a map: its value type, never addressable.
	if extra.Kind() == reflect.Map {
		return w.shape(extra.Elem(), false)
	}
	//: jsontext.Value: any JSON value.
	return &ShapeValue{Kind: Any}
}

// knownShape describes the types whose kind would mislead: time.Time writes a
// date-time string through its own method, time.Duration an integer count of
// nanoseconds, json.Number a number from a string.
func (w *walker) knownShape(t reflect.Type) (*ShapeValue, bool) {
	//: json/v2 gives a time.Duration no default representation: it refuses it.
	if w.v2 && t == durationType {
		//: refused whole.
		return named(t, Unsupported), true
	}
	//: the three types whose kind would mislead.
	switch t {
	//: RFC 3339, through its MarshalJSON.
	case timeType:
		shape := named(t, String)
		shape.Format = "date-time"
		return shape, true
	//: an int64 of nanoseconds.
	case durationType:
		shape := named(t, Integer)
		shape.Format = "duration-ns"
		return shape, true
	//: a string encoding/json writes as a number.
	case numberType:
		return named(t, Number), true
	//: described by its kind or its methods.
	default:
		return nil, false
	}
}

// keyWritable reports whether encoding/json writes t as an object member name:
// a string, an integer or a float — written as text — a type that writes
// itself as text by value, an interface holding one, or a pointer to any of
// these. A json.Marshaler key is not called for a name, and a bool or a struct
// is refused.
func keyWritable(t reflect.Type) bool {
	addressable := false
	//: a pointer key: its element, which a pointer makes addressable.
	if t.Kind() == reflect.Pointer {
		t, addressable = t.Elem(), true
	}
	//: text by value, or by pointer behind a pointer key.
	if writesText(t, addressable) {
		return true
	}
	//: the kinds encoding/json writes as text.
	switch t.Kind() {
	//: written as its text.
	case reflect.String, reflect.Interface,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	//: nothing encoding/json can write as a name.
	default:
		return false
	}
}

// refKind is the kind of a type referenced rather than described again,
// computed without walking it.
func refKind(t reflect.Type) Kind {
	//: the kind a reference stands for.
	switch t.Kind() {
	//: a recursive struct.
	case reflect.Struct:
		return Object
	//: a recursive slice or array.
	case reflect.Slice, reflect.Array:
		return Array
	//: a recursive map.
	case reflect.Map:
		return Map
	//: a pointer to itself writes only null.
	default:
		return Any
	}
}

// named returns a shape of kind that carries t's qualified name —
// "path/to/pkg.Name" — when t is a named type from a package; an unnamed or
// predeclared type has no Name.
func named(t reflect.Type, kind Kind) *ShapeValue {
	shape := &ShapeValue{Kind: kind}
	//: named, and from a package: qualified.
	if t.Name() != "" && t.PkgPath() != "" {
		shape.Name = t.PkgPath() + "." + t.Name()
	}
	return shape
}
