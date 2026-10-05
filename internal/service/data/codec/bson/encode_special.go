package bson

import (
	"encoding/binary"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The encoder's tables.
var (
	// markerTypes maps the payload-free value types to their type byte.
	markerTypes = map[planKind]byte{
		kindMinKey:    typeMinKey,
		kindMaxKey:    typeMaxKey,
		kindUndefined: typeUndefined,
		kindNull:      typeNull,
	}
	// bsonTypeNames names each BSON type for an error message.
	bsonTypeNames = [typeByteValues]string{
		typeDouble:        "double",
		typeString:        "string",
		typeDocument:      "embedded document",
		typeArray:         "array",
		typeBinary:        "binary",
		typeUndefined:     "undefined",
		typeObjectID:      "ObjectID",
		typeBoolean:       "boolean",
		typeDateTime:      "datetime",
		typeNull:          "null",
		typeRegex:         "regex",
		typeDBPointer:     "DBPointer",
		typeJavaScript:    "JavaScript",
		typeSymbol:        "symbol",
		typeCodeWithScope: "code with scope",
		typeInt32:         "int32",
		typeTimestamp:     "timestamp",
		typeInt64:         "int64",
		typeDecimal128:    "decimal128",
		typeMinKey:        "min key",
		typeMaxKey:        "max key",
	}
)

// bsonTypeName names a BSON type for an error message.
func bsonTypeName(t byte) string {
	//: a type byte the table names.
	if name := bsonTypeNames[t]; name != "" {
		//: its name.
		return name
	}
	//: not a BSON type.
	return "type 0x" + strconv.FormatUint(uint64(t), 16)
}

// encodeSpecial writes the exact types the plan matched, and refuses the
// kinds BSON has no form for.
func (e *encoder) encodeSpecial(rv reflect.Value, p *typePlan, st encodeState) (byte, error) {
	//: the dates and the stdlib types.
	if t, handled, err := e.encodeStdlib(rv, p, st); handled {
		//: written.
		return t, err
	}
	//: the value types with a payload of their own.
	if t, handled, err := e.encodeValueType(rv, p); handled {
		//: written.
		return t, err
	}
	//: the text-shaped value types.
	if t, handled, err := e.encodeTextType(rv, p, st); handled {
		//: written.
		return t, err
	}
	//: the payload-free types, and the unsupported kinds.
	return encodeMarker(p)
}

// encodeStdlib writes time.Time, DateTime, url.URL and json.Number.
func (e *encoder) encodeStdlib(rv reflect.Value, p *typePlan, st encodeState) (byte, bool, error) {
	//: one rule per type.
	switch p.kind {
	case kindTime:
		e.appendInt64(int64(NewDateTimeFromTime(timeOf(rv))))
		return typeDateTime, true, nil
	case kindDateTime:
		e.appendInt64(rv.Int())
		return typeDateTime, true, nil
	case kindURL:
		u, _ := reflect.TypeAssert[url.URL](rv)
		return typeString, true, e.appendString(u.String())
	case kindJSONNumber:
		t, err := e.appendJSONNumber(json.Number(rv.String()), st)
		return t, true, err
	default:
		//: none of them.
		return 0, false, nil
	}
}

// encodeValueType writes the codec's value types that carry bytes of their
// own.
func (e *encoder) encodeValueType(rv reflect.Value, p *typePlan) (byte, bool, error) {
	//: one rule per value type.
	switch p.kind {
	case kindObjectID:
		e.appendObjectID(rv)
		return typeObjectID, true, nil
	case kindBinary:
		e.appendBinary(byte(rv.Field(0).Uint()), rv.Field(1).Bytes())
		return typeBinary, true, nil
	case kindTimestamp:
		//: seconds in the high half, the increment in the low one.
		e.buf = binary.LittleEndian.AppendUint64(e.buf, rv.Field(0).Uint()<<32|rv.Field(1).Uint())
		return typeTimestamp, true, nil
	case kindDecimal128:
		e.buf = binary.LittleEndian.AppendUint64(e.buf, rv.Field(1).Uint())
		e.buf = binary.LittleEndian.AppendUint64(e.buf, rv.Field(0).Uint())
		return typeDecimal128, true, nil
	case kindDBPointer:
		return typeDBPointer, true, e.appendDBPointer(rv)
	default:
		//: none of them.
		return 0, false, nil
	}
}

// encodeTextType writes the codec's text-shaped value types.
func (e *encoder) encodeTextType(rv reflect.Value, p *typePlan, st encodeState) (byte, bool, error) {
	//: one rule per value type.
	switch p.kind {
	case kindRegex:
		return typeRegex, true, e.appendRegex(rv.Field(0).String(), rv.Field(1).String())
	case kindJavaScript:
		return typeJavaScript, true, e.appendString(rv.String())
	case kindSymbol:
		return typeSymbol, true, e.appendString(rv.String())
	case kindCodeWithScope:
		return typeCodeWithScope, true, e.appendCodeWithScope(rv, st)
	default:
		//: none of them.
		return 0, false, nil
	}
}

// encodeMarker returns the type of a payload-free value, or refuses a kind BSON
// has no form for.
func encodeMarker(p *typePlan) (byte, error) {
	//: the markers.
	if t, ok := markerTypes[p.kind]; ok {
		//: their type byte, and no payload.
		return t, nil
	}
	//: channels, functions, complex numbers, uintptr, unsafe.Pointer.
	return 0, marshalError(nil, "type "+p.typ.String()+" has no BSON form")
}

// appendJSONNumber writes a json.Number as an integer when it parses as an
// int64, as a double when it parses as a float64, and refuses it otherwise.
func (e *encoder) appendJSONNumber(n json.Number, st encodeState) (byte, error) {
	//: an integer, under the int64 rule.
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		//: minsize applies as to an int64.
		return e.appendInt(i, st.minSize), nil
	}
	f, err := strconv.ParseFloat(string(n), 64)
	//: neither form.
	if err != nil {
		//: refused, without quoting the text.
		return 0, marshalError(err, "a json.Number is neither an int64 nor a float64")
	}
	e.appendDouble(f)
	//: a double.
	return typeDouble, nil
}

// appendObjectID writes the twelve bytes of an ObjectID value.
func (e *encoder) appendObjectID(rv reflect.Value) {
	id, _ := reflect.TypeAssert[ObjectID](rv)
	e.buf = append(e.buf, id[:]...)
}

// appendRegex writes a regex: two cstrings, the options sorted, as the
// specification stores them.
func (e *encoder) appendRegex(pattern, options string) error {
	//: neither cstring may hold a NUL, and both are UTF-8.
	if !validCString(pattern) || !validCString(options) {
		//: refused.
		return marshalError(nil, "a regex holds a NUL byte or invalid UTF-8")
	}
	e.buf = append(e.buf, pattern...)
	e.buf = append(e.buf, 0)
	e.buf = append(e.buf, sortedOptions(options)...)
	e.buf = append(e.buf, 0)
	//: written.
	return nil
}

// validCString reports whether s can be written as a cstring.
func validCString(s string) bool {
	//: no NUL, and UTF-8.
	return strings.IndexByte(s, 0) < 0 && utf8.ValidString(s)
}

// sortedOptions returns options with its characters in ascending order.
func sortedOptions(options string) string {
	//: the common cases need no work.
	if len(options) < 2 {
		//: already sorted.
		return options
	}
	runes := []rune(options)
	slices.Sort(runes)
	//: the sorted options.
	return string(runes)
}

// appendDBPointer writes a DBPointer: its namespace as a string, then its
// ObjectID.
func (e *encoder) appendDBPointer(rv reflect.Value) error {
	//: the namespace is a string.
	if err := e.appendString(rv.Field(0).String()); err != nil {
		//: refused.
		return err
	}
	e.appendObjectID(rv.Field(1))
	//: written.
	return nil
}

// appendCodeWithScope writes a code-with-scope: a total length, the code as a
// string, and the scope, which must encode as a document.
func (e *encoder) appendCodeWithScope(rv reflect.Value, st encodeState) error {
	code := rv.Field(0).String()
	//: the code is a string.
	if !utf8.ValidString(code) {
		//: refused.
		return marshalError(nil, "a CodeWithScope's code is not valid UTF-8")
	}
	scope := rv.Field(1)
	//: a scope is required.
	if scope.IsNil() {
		//: refused, as the previous library refused it.
		return marshalError(nil, "a CodeWithScope has a nil scope")
	}
	start := e.reserveLength()
	e.appendStringBytes(code)
	//: the scope document.
	if err := e.appendScope(scope, st); err != nil {
		//: refused.
		return err
	}
	binary.LittleEndian.PutUint32(e.buf[start:], uint32(len(e.buf)-start))
	//: written.
	return nil
}

// appendScope writes a code-with-scope's scope, which must encode as a
// document; a nil map is the empty one, as at the top level.
func (e *encoder) appendScope(scope reflect.Value, st encodeState) error {
	//: a nil map scope.
	if nilTopLevelMap(scope.Elem()) {
		e.buf = append(e.buf, emptyDocument[:]...)
		//: written.
		return nil
	}
	t, err := e.encodeAnyValue(scope.Interface(), scope.Elem(), st)
	//: the scope could not be encoded.
	if err != nil {
		//: refused.
		return err
	}
	//: the scope is a document.
	if t != typeDocument {
		//: refused.
		return marshalError(nil, "a CodeWithScope's scope encodes as "+bsonTypeName(t)+", not a document")
	}
	//: written.
	return nil
}
