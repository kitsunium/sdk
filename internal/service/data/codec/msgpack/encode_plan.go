// Package msgpack — the per-type encoders. Reflection runs ONCE per Go type:
// encoderFor builds an encodeFunc for the type and caches it, so encoding a
// value is a walk of precomputed closures and, for a struct, an append of
// each key's pre-encoded bytes. A recursive type is handled the way
// encoding/json handles it: a placeholder is cached first and forwards to the
// real encoder once that exists.
//
// A type that encodes itself takes precedence over its kind, in the order the
// vendor-backed codec checked: time.Time (the timestamp extension), then
// MarshalMsgpack() ([]byte, error) — the method the vendor called, kept so a
// type written for it still controls its own bytes — then
// encoding.BinaryMarshaler and encoding.TextMarshaler, both written as a bin,
// as before. A pointer-receiver method is called on the value's address, or on
// an addressable copy when the value has none, so the bytes of a value never
// depend on whether it was passed by pointer.
package msgpack

import (
	"encoding"
	"reflect"
	"sync"
	"time"
)

// encodeFunc appends the encoding of v to b. depth counts the levels already
// entered; a container, a pointer or an interface checks it before going one
// deeper.
type encodeFunc func(b []byte, v reflect.Value, depth int) ([]byte, error)

// emptyFunc reports whether a field value counts as empty for omitempty.
type emptyFunc func(v reflect.Value) bool

// marshalMsgpacker is the method a type implements to write its own
// MessagePack. The returned bytes must be exactly one well-formed value; they
// are checked before they are written.
type marshalMsgpacker interface {
	// MarshalMsgpack returns the value's MessagePack encoding.
	MarshalMsgpack() ([]byte, error)
}

// isZeroer is the method omitempty consults first, as time.Time implements.
type isZeroer interface {
	// IsZero reports whether the value is its type's zero.
	IsZero() bool
}

// listEncoder encodes a slice or an array element by element.
type listEncoder struct {
	// elem encodes one element.
	elem encodeFunc
}

// mapEncoder encodes a map pair by pair, through reflection.
type mapEncoder struct {
	// keyType is the map's key type.
	keyType reflect.Type
	// elemType is the map's element type.
	elemType reflect.Type
	// key encodes one key.
	key encodeFunc
	// elem encodes one element.
	elem encodeFunc
}

// fieldEncoder is one struct field with its encoder.
type fieldEncoder struct {
	// field is the layout entry.
	field *structField
	// enc encodes the field's value.
	enc encodeFunc
	// empty decides omitempty.
	empty emptyFunc
}

// structEncoder encodes a struct as a map, or as an array with as_array.
type structEncoder struct {
	// fields are the struct's keys in encode order.
	fields []fieldEncoder
	// asArray writes an array of every field instead of a map.
	asArray bool
	// hasOmitEmpty takes the counting path that leaves empty fields out.
	hasOmitEmpty bool
}

// Plan cache and the types the builders compare against.
var (
	// encoders caches encodeFunc per reflect.Type.
	encoders sync.Map
	// timeType is time.Time, encoded as the timestamp extension.
	timeType = reflect.TypeFor[time.Time]()
	// errorType is the error interface, encoded as its message.
	errorType = reflect.TypeFor[error]()
	// isZeroerType is the IsZero method omitempty consults.
	isZeroerType = reflect.TypeFor[isZeroer]()
	// marshalMsgpackerType is the MarshalMsgpack method.
	marshalMsgpackerType = reflect.TypeFor[marshalMsgpacker]()
	// binaryMarshalerType is encoding.BinaryMarshaler.
	binaryMarshalerType = reflect.TypeFor[encoding.BinaryMarshaler]()
	// textMarshalerType is encoding.TextMarshaler.
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

// encoderFor returns the cached encoder of t, building it on first use.
func encoderFor(t reflect.Type) encodeFunc {
	//: fast path: built before.
	if cached, ok := encoders.Load(t); ok {
		//: the cache only ever holds encodeFunc.
		if f, isFunc := cached.(encodeFunc); isFunc {
			//: hit.
			return f
		}
	}
	//: a recursive type reaches itself while being built: cache a
	//: placeholder that waits for the real encoder, then build.
	var (
		wg    sync.WaitGroup
		built encodeFunc
	)
	wg.Add(1)
	placeholder := encodeFunc(func(b []byte, v reflect.Value, depth int) ([]byte, error) {
		wg.Wait()
		//: forward to the finished encoder.
		return built(b, v, depth)
	})
	//: another goroutine may have started first; use its entry.
	if actual, loaded := encoders.LoadOrStore(t, placeholder); loaded {
		//: the cache only ever holds encodeFunc.
		if f, isFunc := actual.(encodeFunc); isFunc {
			//: theirs.
			return f
		}
	}
	built = buildEncoder(t)
	wg.Done()
	encoders.Store(t, built)
	//: the finished encoder.
	return built
}

// buildEncoder chooses the encoder of t: a self-encoding type first, then its
// kind.
func buildEncoder(t reflect.Type) encodeFunc {
	//: a type that writes itself wins over its kind.
	if f := hookEncoder(t); f != nil {
		//: time, MarshalMsgpack, BinaryMarshaler or TextMarshaler.
		return f
	}
	//: scalars need no plan.
	if f := scalarEncoder(t.Kind()); f != nil {
		//: bool, number or string.
		return f
	}
	//: containers, pointers, interfaces, or an unsupported kind.
	return compositeEncoder(t)
}

// hookEncoder returns the encoder of a type that encodes itself, or nil.
// Pointers and interfaces defer to what they hold.
func hookEncoder(t reflect.Type) encodeFunc {
	//: a pointer or interface is not itself the self-encoding value.
	if t.Kind() == reflect.Pointer || t.Kind() == reflect.Interface {
		//: decided on the element.
		return nil
	}
	//: the timestamp extension.
	if t == timeType {
		//: time.Time.
		return encodeTime
	}
	//: value-receiver methods.
	if f := methodEncoder(t); f != nil {
		//: callable on the value itself.
		return f
	}
	//: pointer-receiver methods need an address.
	if f := methodEncoder(reflect.PointerTo(t)); f != nil {
		//: through the address, or an addressable copy.
		return addressEncoder(f)
	}
	//: an ordinary value.
	return nil
}

// methodEncoder returns the encoder calling the first self-encoding method t
// implements, in the vendor's order, or nil.
func methodEncoder(t reflect.Type) encodeFunc {
	//: first match wins.
	switch {
	//: the MessagePack-specific method.
	case t.Implements(marshalMsgpackerType):
		return encodeSelfMsgpack
	//: binary form, written as a bin.
	case t.Implements(binaryMarshalerType):
		return encodeSelfBinary
	//: text form, also written as a bin — the vendor's choice, kept.
	case t.Implements(textMarshalerType):
		return encodeSelfText
	//: no method.
	default:
		return nil
	}
}

// scalarEncoder returns the encoder of a bool, number or string kind, or nil.
func scalarEncoder(k reflect.Kind) encodeFunc {
	//: one encoder per kind family.
	switch k {
	//: true or false.
	case reflect.Bool:
		return encodeBool
	//: every signed width shares the shortest-form writer.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encodeIntValue
	//: every unsigned width too, uintptr included.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return encodeUintValue
	//: float 32 keeps its width.
	case reflect.Float32:
		return encodeFloat32Value
	//: float 64.
	case reflect.Float64:
		return encodeFloat64Value
	//: str.
	case reflect.String:
		return encodeStringValue
	//: not a scalar.
	default:
		return nil
	}
}

// compositeEncoder returns the encoder of a container, pointer or interface
// kind, and a refusal for the kinds MessagePack cannot carry.
func compositeEncoder(t reflect.Type) encodeFunc {
	//: one builder per kind.
	switch t.Kind() {
	//: []byte is a bin; any other slice an array.
	case reflect.Slice:
		return sliceEncoder(t)
	//: [N]byte is a bin; any other array an array.
	case reflect.Array:
		return arrayEncoder(t)
	//: a map, keys and values by their own encoders.
	case reflect.Map:
		return newMapEncoder(t)
	//: a struct, through its cached layout.
	case reflect.Struct:
		return newStructEncoder(t)
	//: nil, or what it points at.
	case reflect.Pointer:
		return pointerEncoder(t)
	//: nil, the error's message, or the dynamic value.
	case reflect.Interface:
		return interfaceEncoder(t)
	//: chan, func, complex, unsafe.Pointer.
	default:
		return failingEncoder(unsupportedType(t))
	}
}

// failingEncoder returns an encoder that always fails with err.
func failingEncoder(err error) encodeFunc {
	//: the failure is decided once, when the plan is built.
	return func(b []byte, _ reflect.Value, _ int) ([]byte, error) {
		//: b unchanged.
		return b, err
	}
}

// encodeBool writes a bool kind.
func encodeBool(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: one byte.
	return appendBool(b, v.Bool()), nil
}

// encodeIntValue writes a signed kind in its shortest form.
func encodeIntValue(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: the Go width is not on the wire.
	return appendInt(b, v.Int()), nil
}

// encodeUintValue writes an unsigned kind in its shortest form.
func encodeUintValue(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: the Go width is not on the wire.
	return appendUint(b, v.Uint()), nil
}

// encodeFloat32Value writes a float32 kind as float 32.
func encodeFloat32Value(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: single precision is exact for a float32.
	return appendFloat32(b, float32(v.Float())), nil
}

// encodeFloat64Value writes a float64 kind as float 64.
func encodeFloat64Value(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: double precision.
	return appendFloat64(b, v.Float()), nil
}

// encodeStringValue writes a string kind as str.
func encodeStringValue(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: the bytes as they are.
	return appendString(b, v.String())
}

// encodeTime writes a time.Time as the timestamp extension.
func encodeTime(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: a time reached through an unexported embedding cannot be read.
	t, ok := interfaceOf[time.Time](v)
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	//: the shortest timestamp form.
	return appendTimestamp(b, t), nil
}

// sliceEncoder builds the encoder of slice type t.
func sliceEncoder(t reflect.Type) encodeFunc {
	//: any slice of a byte kind is a bin, named element types included.
	if t.Elem().Kind() == reflect.Uint8 {
		//: bin, or nil.
		return encodeByteSlice
	}
	//: an array of elements, or nil.
	return (&listEncoder{elem: encoderFor(t.Elem())}).encodeSlice
}

// arrayEncoder builds the encoder of array type t.
func arrayEncoder(t reflect.Type) encodeFunc {
	//: [N]byte is a bin.
	if t.Elem().Kind() == reflect.Uint8 {
		//: bin.
		return encodeByteArray
	}
	//: an array of N elements.
	return (&listEncoder{elem: encoderFor(t.Elem())}).encodeList
}

// encodeByteSlice writes a byte slice as bin, a nil one as nil.
func encodeByteSlice(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: nil and empty differ on the wire.
	if v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	//: bin.
	return appendBinary(b, v.Bytes())
}

// encodeByteArray writes a byte array as bin.
func encodeByteArray(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: an addressable array is read in place.
	if v.CanAddr() {
		//: bin of the array's bytes.
		return appendBinary(b, v.Bytes())
	}
	n := v.Len()
	//: a length the wire cannot carry.
	if err := checkLength(n); err != nil {
		//: b unchanged.
		return b, err
	}
	b = appendLength(b, n, &binForms)
	//: a value copy has no address, so its bytes are read one by one.
	for i := range n {
		b = append(b, byte(v.Index(i).Uint()))
	}
	//: bin.
	return b, nil
}

// encodeSlice writes a slice as an array, a nil one as nil.
func (l *listEncoder) encodeSlice(b []byte, v reflect.Value, depth int) ([]byte, error) {
	//: nil and empty differ on the wire.
	if v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	//: an array of the elements.
	return l.encodeList(b, v, depth)
}

// encodeList writes the elements of a slice or array as an array.
func (l *listEncoder) encodeList(b []byte, v reflect.Value, depth int) ([]byte, error) {
	//: one level deeper.
	if depth >= maxDepth {
		//: a cycle through slices, or absurd nesting.
		return b, depthExceeded(true)
	}
	n := v.Len()
	b, err := appendArrayHeader(b, n)
	//: refuse a count the wire cannot carry.
	if err != nil {
		//: b unchanged.
		return b, err
	}
	//: each element by the element encoder.
	for i := range n {
		b, err = l.elem(b, v.Index(i), depth+1)
		if err != nil {
			//: the first failure wins.
			return b, err
		}
	}
	//: the whole array.
	return b, nil
}

// pointerEncoder builds the encoder of pointer type t: nil, or the element.
func pointerEncoder(t reflect.Type) encodeFunc {
	elem := encoderFor(t.Elem())
	//: dereference one level per call.
	return func(b []byte, v reflect.Value, depth int) ([]byte, error) {
		//: a nil pointer is nil.
		if v.IsNil() {
			//: nil.
			return appendNil(b), nil
		}
		//: a pointer chain can cycle, so it counts as a level.
		if depth >= maxDepth {
			//: refuse instead of recursing forever.
			return b, depthExceeded(true)
		}
		//: what it points at.
		return elem(b, v.Elem(), depth+1)
	}
}

// interfaceEncoder builds the encoder of interface type t.
func interfaceEncoder(t reflect.Type) encodeFunc {
	//: a field of type error is written as its message.
	if t == errorType {
		//: str, or nil.
		return encodeError
	}
	//: anything else by its dynamic type.
	return encodeInterface
}

// encodeInterface writes an interface value by its dynamic type.
func encodeInterface(b []byte, v reflect.Value, depth int) ([]byte, error) {
	//: a nil interface is nil.
	if v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	//: an interface can hold a cycle, so it counts as a level.
	if depth >= maxDepth {
		//: refuse instead of recursing forever.
		return b, depthExceeded(true)
	}
	//: the common dynamic types skip the plan lookup.
	if v.CanInterface() {
		//: the value inside, through the untyped fast paths.
		return appendAny(b, v.Interface(), depth+1)
	}
	e := v.Elem()
	//: reached through an unexported embedding: the plan of the element.
	return encoderFor(e.Type())(b, e, depth+1)
}

// encodeError writes an error-typed value as its message, nil as nil.
func encodeError(b []byte, v reflect.Value, _ int) ([]byte, error) {
	//: a nil error is nil.
	if v.IsNil() {
		//: nil.
		return appendNil(b), nil
	}
	err, ok := interfaceOf[error](v)
	//: a value reached through an unexported embedding cannot be called.
	if !ok {
		//: refuse rather than panic.
		return b, readOnly(v.Type())
	}
	//: the message, as str.
	return appendString(b, err.Error())
}

// interfaceOf returns v as T when the value may be handed out — it was not
// reached through an unexported field — and holds a T.
func interfaceOf[T any](v reflect.Value) (T, bool) {
	var zero T
	//: reflect panics on Interface() for a read-only value.
	if !v.CanInterface() {
		//: the caller refuses.
		return zero, false
	}
	//: the assertion, without boxing a value that already is a T.
	return reflect.TypeAssert[T](v)
}

// readOnly is the encode failure for a value the codec may not call a method
// on, because it was reached through an unexported embedded field.
func readOnly(t reflect.Type) error {
	//: name the type, never the value.
	return marshalFault("cannot call the encoding method of a value reached through an unexported field", typeField(t))
}
