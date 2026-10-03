// Package bson — the encoder's entry point and dispatch. It appends one
// document to a byte slice, reflection planned once per type, the common
// interface values taken by a type switch instead. Each element's type byte is
// written as a placeholder and set once the value has said what it encodes as;
// each document's length is reserved and filled in when the document ends.
package bson

import (
	"reflect"
)

// encoder appends BSON to buf.
type encoder struct {
	// buf is the output so far.
	buf []byte
}

// encodeState is what a value's encoding depends on besides the value.
type encodeState struct {
	// depth is the nesting level of the document the value is written in; a
	// container the value opens is one deeper.
	depth int
	// hops counts the pointers and interfaces followed since the last
	// container, so a pointer cycle that opens none is still refused.
	hops int
	// minSize writes integers that fit as int32.
	minSize bool
	// addressable mirrors reflect's addressability along the path the
	// previous library walked, which decides whether a MarshalBSON method
	// with a pointer receiver is called: a map value, an interface's dynamic
	// value and a value passed to Marshal by value are not addressable.
	addressable bool
}

// child returns the state of a value inside a container this state opens.
func (st encodeState) child() encodeState {
	//: one level deeper, hops reset, minsize inherited.
	return encodeState{depth: st.depth + 1, minSize: st.minSize, addressable: st.addressable}
}

// appendDocument appends the document v encodes as to dst. On failure dst is
// returned with its original length.
func appendDocument(dst []byte, v any) ([]byte, error) {
	origLen := len(dst)
	//: nil is not a document.
	if v == nil {
		//: refused.
		return dst, marshalError(nil, "a nil value has no document form")
	}
	e := encoder{buf: dst}
	err := e.encodeRoot(v)
	//: a failure leaves the caller's bytes as they were.
	if err != nil {
		//: truncated back.
		return dst[:origLen], err
	}
	//: the document appended.
	return e.buf, nil
}

// encodeRoot writes the top-level value, which must encode as a document.
func (e *encoder) encodeRoot(v any) error {
	rv := reflect.ValueOf(v)
	//: a map that is nil at the top level is the empty document, as the
	//: previous library wrote it; anywhere else it is null.
	if nilTopLevelMap(rv) {
		e.buf = append(e.buf, emptyDocument[:]...)
		//: written.
		return nil
	}
	t, err := e.encodeValue(rv, planFor(rv.Type()), encodeState{addressable: rv.CanAddr()})
	//: the value itself could not be encoded.
	if err != nil {
		//: refused.
		return err
	}
	//: BSON's top level is a document and nothing else.
	if t != typeDocument {
		//: refused, naming what the value would have been.
		return marshalError(nil, "the top level must be a document; "+rv.Type().String()+" encodes as "+bsonTypeName(t))
	}
	//: written.
	return nil
}

// nilTopLevelMap reports a top-level nil map, followed through non-nil
// pointers that have no MarshalBSON of their own.
func nilTopLevelMap(rv reflect.Value) bool {
	//: follow pointers, as the encoder would.
	for rv.Kind() == reflect.Pointer && !rv.IsNil() && planFor(rv.Type()).marshaler == hookNone {
		rv = rv.Elem()
	}
	//: a nil map whose own type does not write itself.
	return rv.Kind() == reflect.Map && rv.IsNil() && planFor(rv.Type()).marshaler == hookNone
}

// encodeValue appends the encoding of rv, planned by p, and returns its BSON
// type for the caller to put in the element's type byte.
func (e *encoder) encodeValue(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	//: the type's own MarshalBSON comes first.
	if p.marshaler != hookNone {
		//: called when reachable; otherwise the kind decides.
		if t, handled, err := e.marshalHook(rv, p, st); handled {
			//: the hook's document, or its failure.
			return t, err
		}
	}
	//: integers.
	if t, handled, err := e.encodeInteger(rv, p, st); handled {
		//: written.
		return t, err
	}
	//: the other scalars.
	if t, handled, err := e.encodeScalar(rv, p); handled {
		//: written.
		return t, err
	}
	//: containers and indirections.
	if t, handled, err := e.encodeComposite(rv, p, st); handled {
		//: written.
		return t, err
	}
	//: the codec's own value types and the stdlib types it knows.
	return e.encodeSpecial(rv, p, st)
}

// marshalHook calls MarshalBSON when p's method is reachable from rv: always
// for a value receiver, on an addressable value for a pointer receiver. A nil
// pointer or interface writes null without calling it.
func (e *encoder) marshalHook(rv reflect.Value, p *typePlan, st encodeState) (byte, bool, error) {
	//: a pointer receiver needs an address.
	if p.marshaler == hookPointer && (!st.addressable || !rv.CanAddr()) {
		//: not reachable: the kind mapping applies.
		return 0, false, nil
	}
	//: nothing to call the method on.
	if (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) && rv.IsNil() {
		//: null.
		return typeNull, true, nil
	}
	m, ok := methodValue(rv).(interface{ MarshalBSON() ([]byte, error) })
	//: a nil interface's dynamic value has no method.
	if !ok {
		//: null.
		return typeNull, true, nil
	}
	data, err := m.MarshalBSON()
	//: the method's own failure.
	if err != nil {
		//: wrapped; an SDK error keeps its code.
		return 0, true, marshalError(err, "MarshalBSON of "+p.typ.String()+" failed")
	}
	//: what it returned must be one well-formed document, within the depth left.
	if verr := validateEmbedded(data, st.depth+1); verr != nil {
		//: refused rather than spliced in.
		return 0, true, verr
	}
	e.buf = append(e.buf, data...)
	//: an embedded document.
	return typeDocument, true, nil
}

// validateEmbedded checks a document a MarshalBSON method returned, to be
// written at depth.
func validateEmbedded(data []byte, depth int) error {
	v := validator{}
	//: the shape every document has.
	if len(data) < minDocumentSize || int64(readInt32(data)) != int64(len(data)) {
		//: not one document.
		return marshalError(nil, "MarshalBSON returned bytes that are not one BSON document")
	}
	problem := v.document(data, 0, depth)
	//: well formed.
	if problem == nil {
		//: accepted.
		return nil
	}
	//: too deep.
	if problem.depth {
		//: the nesting bound.
		return depthError("Marshal")
	}
	//: malformed.
	return marshalError(nil, "MarshalBSON returned malformed BSON: "+problem.rule)
}

// methodValue returns rv as an interface whose method set includes rv's: an
// interface's dynamic value or a pointer as it is, a pointer to any other
// addressable value (which boxes without allocating), or the value itself.
func methodValue(rv reflect.Value) any {
	//: an interface holds its own dynamic value; a pointer is its own receiver.
	if rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer {
		//: as it is.
		return rv.Interface()
	}
	//: an address carries the value's methods and the pointer's.
	if rv.CanAddr() {
		//: the pointer.
		return rv.Addr().Interface()
	}
	//: the value itself.
	return rv.Interface()
}

// encodeInteger writes the integer kinds.
func (e *encoder) encodeInteger(rv reflect.Value, p *typePlan, st encodeState) (byte, bool, error) {
	//: one rule per integer mapping.
	switch p.kind {
	case kindInt32:
		e.appendInt32(int32(rv.Int()))
		return typeInt32, true, nil
	case kindInt:
		return e.appendInt(rv.Int(), true), true, nil
	case kindInt64:
		return e.appendInt(rv.Int(), st.minSize), true, nil
	case kindUint16:
		e.appendInt32(int32(rv.Uint()))
		return typeInt32, true, nil
	case kindUint64:
		t, err := e.appendUint(rv.Uint(), st.minSize, p)
		return t, true, err
	default:
		//: not an integer.
		return 0, false, nil
	}
}

// encodeScalar writes booleans, floats and strings.
func (e *encoder) encodeScalar(rv reflect.Value, p *typePlan) (byte, bool, error) {
	//: one rule per scalar mapping.
	switch p.kind {
	case kindBool:
		e.appendBool(rv.Bool())
		return typeBoolean, true, nil
	case kindFloat32, kindFloat64:
		e.appendDouble(rv.Float())
		return typeDouble, true, nil
	case kindString:
		return typeString, true, e.appendString(rv.String())
	default:
		//: not one of them.
		return 0, false, nil
	}
}

// encodeComposite writes the containers and follows the indirections.
func (e *encoder) encodeComposite(rv reflect.Value, p *typePlan, st encodeState) (byte, bool, error) {
	//: one rule per container mapping.
	switch p.kind {
	case kindBytes, kindByteSlice:
		t := e.encodeBytes(rv)
		return t, true, nil
	case kindByteArray:
		e.appendByteArray(rv)
		return typeBinary, true, nil
	case kindSlice, kindD, kindDocSlice, kindMap:
		t, err := e.encodeNilable(rv, p, st)
		return t, true, err
	case kindArray, kindDocArray, kindStruct:
		t, err := e.encodeContainer(rv, p, st)
		return t, true, err
	case kindPointer, kindAny, kindInterface:
		t, err := e.encodeIndirect(rv, p, st)
		return t, true, err
	default:
		//: not a container.
		return 0, false, nil
	}
}

// encodeBytes writes a byte slice as a generic binary, a nil one as null.
func (e *encoder) encodeBytes(rv reflect.Value) byte {
	//: nil is null.
	if rv.IsNil() {
		//: no payload.
		return typeNull
	}
	e.appendBinary(BinaryGeneric, rv.Bytes())
	//: written.
	return typeBinary
}

// encodeNilable writes a slice or map, or null when it is nil.
func (e *encoder) encodeNilable(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	//: nil is null.
	if rv.IsNil() {
		//: no payload.
		return typeNull, nil
	}
	//: the container.
	return e.encodeContainer(rv, p, st)
}

// encodeIndirect follows a pointer or an interface to the value it holds.
func (e *encoder) encodeIndirect(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	//: nil is null.
	if rv.IsNil() {
		//: no payload.
		return typeNull, nil
	}
	st.hops++
	//: a chain of indirections that never reaches a value.
	if st.hops > maxIndirections {
		//: a pointer cycle; refused as the nesting bound.
		return 0, depthError("Marshal")
	}
	//: a pointer's target is addressable.
	if p.kind == kindPointer {
		st.addressable = true
		//: the planned target.
		return e.encodeValue(rv.Elem(), p.elem, st)
	}
	elem := rv.Elem()
	//: an interface's dynamic value, planned by its own type.
	return e.encodeAnyValue(elem.Interface(), elem, st)
}

// encodeAnyValue writes a dynamic value: the common types through a type
// switch, every other one through its plan. reflected is the value as a
// reflect.Value when the caller already has one, the zero Value otherwise.
func (e *encoder) encodeAnyValue(v any, reflected reflect.Value, st encodeState) (byte, error) {
	//: the scalars a schemaless document is made of.
	if t, handled, err := e.encodeCommonScalar(v, st); handled {
		//: written.
		return t, err
	}
	//: the containers a decode into an interface produces.
	if t, handled, err := e.encodeCommonContainer(v, st); handled {
		//: written.
		return t, err
	}
	//: anything else, planned.
	if !reflected.IsValid() {
		reflected = reflect.ValueOf(v)
	}
	//: an interface's dynamic value is not addressable.
	return e.encodeValue(reflected, planFor(reflected.Type()), encodeState{depth: st.depth, hops: st.hops, minSize: st.minSize})
}

// encodeCommonScalar writes, without reflection, the scalar dynamic types;
// each rule is the plan's rule for the same type.
func (e *encoder) encodeCommonScalar(v any, st encodeState) (byte, bool, error) {
	//: one case per type.
	switch x := v.(type) {
	case nil:
		return typeNull, true, nil
	case string:
		return typeString, true, e.appendString(x)
	case bool:
		e.appendBool(x)
		return typeBoolean, true, nil
	case int:
		return e.appendInt(int64(x), true), true, nil
	case int32:
		e.appendInt32(x)
		return typeInt32, true, nil
	case int64:
		return e.appendInt(x, st.minSize), true, nil
	case float64:
		e.appendDouble(x)
		return typeDouble, true, nil
	default:
		//: not one of them.
		return 0, false, nil
	}
}

// encodeCommonContainer writes, without reflection, the container dynamic
// types a decode into an interface produces.
func (e *encoder) encodeCommonContainer(v any, st encodeState) (byte, bool, error) {
	//: one case per type.
	switch x := v.(type) {
	case map[string]any:
		return e.encodeCommonMap(x, st)
	case M:
		return e.encodeCommonMap(x, st)
	case D:
		return e.encodeCommonD(x, st)
	case A:
		return e.encodeCommonArray(x, st)
	case []any:
		return e.encodeCommonArray(x, st)
	default:
		//: not one of them.
		return 0, false, nil
	}
}

// openContainer checks the depth bound for a container st opens and reserves
// its length; it returns the child state and where the length goes.
func (e *encoder) openContainer(st encodeState) (encodeState, int, error) {
	child := st.child()
	//: the bound is checked before anything nested is written.
	if child.depth > maxBSONNestedLevels {
		//: too deep, or cyclic.
		return child, 0, depthError("Marshal")
	}
	//: the length placeholder.
	return child, e.reserveLength(), nil
}

// encodeCommonMap writes a map[string]any, or nil as null, its keys sorted.
func (e *encoder) encodeCommonMap(m map[string]any, st encodeState) (byte, bool, error) {
	//: nil is null.
	if m == nil {
		//: no payload.
		return typeNull, true, nil
	}
	child, start, err := e.openContainer(st)
	//: too deep.
	if err != nil {
		//: refused.
		return 0, true, err
	}
	child.addressable = false
	segs := segmentPool.Get()
	defer segmentPool.Put(segs)
	//: every entry, in the map's order for now.
	for key, value := range m {
		begin := len(e.buf)
		keyEnd, err := e.beginElement(key)
		//: an element name BSON cannot hold.
		if err != nil {
			//: refused.
			return 0, true, err
		}
		//: a value that cannot be encoded.
		if err := e.finishElement(begin, value, child); err != nil {
			//: refused.
			return 0, true, err
		}
		*segs = append(*segs, segment{start: begin, keyEnd: keyEnd, end: len(e.buf)})
	}
	e.sortSegments(*segs)
	//: the terminator and the length.
	return typeDocument, true, e.endDocument(start)
}

// encodeCommonD writes a D in order, or nil as null.
func (e *encoder) encodeCommonD(d D, st encodeState) (byte, bool, error) {
	//: nil is null.
	if d == nil {
		//: no payload.
		return typeNull, true, nil
	}
	child, start, err := e.openContainer(st)
	//: too deep.
	if err != nil {
		//: refused.
		return 0, true, err
	}
	child.addressable = false
	//: in order: the order is the point of a D.
	for _, el := range d {
		begin := len(e.buf)
		//: an element name BSON cannot hold.
		if _, err := e.beginElement(el.Key); err != nil {
			//: refused.
			return 0, true, err
		}
		//: a value that cannot be encoded.
		if err := e.finishElement(begin, el.Value, child); err != nil {
			//: refused.
			return 0, true, err
		}
	}
	//: the terminator and the length.
	return typeDocument, true, e.endDocument(start)
}

// encodeCommonArray writes a []any, or nil as null.
func (e *encoder) encodeCommonArray(a []any, st encodeState) (byte, bool, error) {
	//: nil is null.
	if a == nil {
		//: no payload.
		return typeNull, true, nil
	}
	child, start, err := e.openContainer(st)
	//: too deep.
	if err != nil {
		//: refused.
		return 0, true, err
	}
	child.addressable = false
	//: keys "0", "1", … in order.
	for i, value := range a {
		//: a value that cannot be encoded.
		if err := e.finishElement(e.beginIndex(i), value, child); err != nil {
			//: refused.
			return 0, true, err
		}
	}
	//: the terminator and the length.
	return typeArray, true, e.endDocument(start)
}

// finishElement writes a dynamic value after an element header begun at
// begin, and sets the header's type byte.
func (e *encoder) finishElement(begin int, value any, st encodeState) error {
	t, err := e.encodeAnyValue(value, reflect.Value{}, st)
	//: a value that cannot be encoded.
	if err != nil {
		//: refused.
		return err
	}
	e.buf[begin] = t
	//: written.
	return nil
}
