// Package bson — decoding into an interface: the Go value each BSON type
// becomes when nothing else says what it should be. The table is the previous
// library's, its types replaced by this package's: a double is a float64, an
// int32 an int32, an int64 an int64, a string a string, a boolean a bool, a
// null nil, an array an A, a datetime a DateTime, a binary a Binary, and so
// on. A document becomes the ancestor type when one is set — the type of the
// nearest enclosing map[string]any, M or slice of E — and a D otherwise, so a
// document decoded into *any is a D all the way down while one decoded into a
// map[string]any is maps all the way down.
package bson

import (
	"bytes"
	"reflect"
)

// decodeAny decodes one value as an interface would hold it.
func decodeAny(t byte, val []byte, st decodeState) (any, error) {
	//: the containers.
	switch t {
	case typeDocument:
		return decodeAnyDocument(val, st)
	case typeArray:
		return decodeAnyArray(val, st)
	case typeCodeWithScope:
		return decodeAnyCodeWithScope(val, st)
	}
	//: the scalars.
	return decodeAnyScalar(t, val)
}

// decodeAnyScalar decodes the BSON types that hold no other value.
func decodeAnyScalar(t byte, val []byte) (any, error) {
	//: the fixed-width types.
	if value, ok := decodeAnyFixed(t, val); ok {
		//: decoded.
		return value, nil
	}
	//: the variable-width types.
	switch t {
	case typeString, typeJavaScript, typeSymbol:
		return anyText(t, val)
	case typeBinary:
		subtype, data, ok := binaryPayload(val)
		//: changed under us.
		if !ok {
			//: refused.
			return nil, errCorrupt()
		}
		return Binary{Subtype: subtype, Data: bytes.Clone(data)}, nil
	case typeRegex:
		return anyRegex(val)
	case typeDBPointer:
		return anyDBPointer(val)
	}
	//: the validator refuses every other type byte.
	return nil, errCorrupt()
}

// decodeAnyFixed decodes the fixed-width BSON types, whose length the element
// walk has already checked.
func decodeAnyFixed(t byte, val []byte) (any, bool) {
	//: the numbers and the booleans.
	if value, ok := decodeAnyNumber(t, val); ok {
		//: decoded.
		return value, true
	}
	//: the payload-free types.
	if value, ok := decodeAnyMarker(t); ok {
		//: decoded.
		return value, true
	}
	//: one Go type per BSON type.
	switch t {
	case typeObjectID:
		var id ObjectID
		copy(id[:], val)
		return id, true
	case typeTimestamp:
		return Timestamp{T: readUint32(val[lengthSize:]), I: readUint32(val)}, true
	case typeDecimal128:
		return Decimal128{l: uint64(readInt64(val)), h: uint64(readInt64(val[wordSize:]))}, true
	default:
		//: not fixed-width.
		return nil, false
	}
}

// decodeAnyMarker decodes the BSON types that carry no payload.
func decodeAnyMarker(t byte) (any, bool) {
	//: one Go value per BSON type.
	switch t {
	case typeNull:
		return nil, true
	case typeUndefined:
		return Undefined{}, true
	case typeMinKey:
		return MinKey{}, true
	case typeMaxKey:
		return MaxKey{}, true
	default:
		//: a type with a payload.
		return nil, false
	}
}

// decodeAnyNumber decodes the numeric types, booleans and datetimes.
func decodeAnyNumber(t byte, val []byte) (any, bool) {
	//: one Go type per BSON type.
	switch t {
	case typeDouble:
		return readDouble(val), true
	case typeInt32:
		return readInt32(val), true
	case typeInt64:
		return readInt64(val), true
	case typeBoolean:
		return val[0] == 1, true
	case typeDateTime:
		return DateTime(readInt64(val)), true
	default:
		//: not a number.
		return nil, false
	}
}

// anyText decodes a string, a JavaScript or a symbol into its Go type.
func anyText(t byte, val []byte) (any, error) {
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	//: one Go type per BSON type.
	switch t {
	case typeJavaScript:
		return JavaScript(text), nil
	case typeSymbol:
		return Symbol(text), nil
	}
	//: a string.
	return string(text), nil
}

// anyRegex decodes a regex: two cstrings.
func anyRegex(val []byte) (any, error) {
	pattern := cstringEnd(val)
	//: changed under us.
	if pattern < 0 {
		//: refused.
		return nil, errCorrupt()
	}
	options := cstringEnd(val[pattern+1:])
	//: changed under us.
	if options < 0 {
		//: refused.
		return nil, errCorrupt()
	}
	//: the options as they were written; an encode sorts them.
	return Regex{Pattern: string(val[:pattern]), Options: string(val[pattern+1 : pattern+1+options])}, nil
}

// anyDBPointer decodes a DBPointer: a string, then twelve bytes.
func anyDBPointer(val []byte) (any, error) {
	size := prefixedSize(val, lengthSize)
	//: changed under us.
	if size < 0 || len(val)-size < objectIDSize {
		//: refused.
		return nil, errCorrupt()
	}
	ns, ok := stringPayload(val[:size])
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	var id ObjectID
	copy(id[:], val[size:])
	//: both parts.
	return DBPointer{DB: string(ns), Pointer: id}, nil
}

// decodeAnyDocument decodes a document as the ancestor type, or as a D.
func decodeAnyDocument(val []byte, st decodeState) (any, error) {
	//: one shape per ancestor.
	switch st.ancestor {
	case nil, typeOfD:
		st.ancestor = typeOfD
		return decodeDocument(val, nil, st)
	case typeOfStringAnyMap:
		m := make(map[string]any, countElements(val))
		return m, fillStringAnyMap(val, m, st)
	case typeOfM:
		m := make(M, countElements(val))
		return m, fillStringAnyMap(val, m, st)
	}
	//: another named map or slice of E, through its plan.
	target := reflect.New(st.ancestor).Elem()
	//: the ancestor's own rules apply.
	if err := decodeValue(typeDocument, val, target, planFor(st.ancestor), st); err != nil {
		//: refused.
		return nil, err
	}
	//: the decoded value.
	return target.Interface(), nil
}

// decodeDocument decodes a document into a D, appending to dst.
func decodeDocument(val []byte, dst D, st decodeState) (D, error) {
	//: a fresh D sized by the element count.
	if dst == nil {
		dst = make(D, 0, countElements(val))
	}
	it := newDocIter(val)
	//: element by element, in order.
	for {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return dst, err
		}
		next, err := decodeDElement(el, st)
		//: the value's own failure.
		if err != nil {
			//: refused.
			return nil, err
		}
		dst = append(dst, next)
	}
}

// decodeDElement decodes one element of a D.
func decodeDElement(el element, st decodeState) (E, error) {
	value, err := decodeAny(el.typ, el.value, st)
	//: the value's own failure.
	if err != nil {
		//: refused.
		return E{}, err
	}
	//: the name, copied out of the input, and the value.
	return E{Key: string(el.key), Value: value}, nil
}

// decodeAnyArray decodes an array into an A, its elements under the same
// ancestor.
func decodeAnyArray(val []byte, st decodeState) (any, error) {
	out := make(A, 0, countElements(val))
	it := newDocIter(val)
	//: element by element, the keys ignored.
	for {
		el, more, err := it.next()
		//: the end, or bytes that changed.
		if !more || err != nil {
			//: decoded.
			return out, err
		}
		value, err := decodeAny(el.typ, el.value, st)
		//: the value's own failure.
		if err != nil {
			//: refused.
			return nil, err
		}
		out = append(out, value)
	}
}

// decodeAnyCodeWithScope decodes a code-with-scope: its code, and its scope
// as a D whose own documents follow the current ancestor.
func decodeAnyCodeWithScope(val []byte, st decodeState) (any, error) {
	//: the total length, then the code string.
	if len(val) < lengthSize {
		//: changed under us.
		return nil, errCorrupt()
	}
	codeSize := prefixedSize(val[lengthSize:], lengthSize)
	//: changed under us.
	if codeSize < 0 {
		//: refused.
		return nil, errCorrupt()
	}
	code, ok := stringPayload(val[lengthSize : lengthSize+codeSize])
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	scope, err := decodeDocument(val[lengthSize+codeSize:], nil, st)
	//: the scope's own failure.
	if err != nil {
		//: refused.
		return nil, err
	}
	//: both parts.
	return CodeWithScope{Code: JavaScript(code), Scope: scope}, nil
}
