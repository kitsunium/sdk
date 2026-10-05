package bson

import (
	"net/url"
	"reflect"
	"strconv"
	"time"
)

// timeStringLayout is the layout a BSON string decodes into a time.Time by,
// the one the previous library accepted.
const timeStringLayout string = "2006-01-02T15:04:05.999Z07:00"

// valueTypeBytes maps each value type to the BSON type it is read from.
var valueTypeBytes = map[planKind]byte{
	kindObjectID:      typeObjectID,
	kindDateTime:      typeDateTime,
	kindBinary:        typeBinary,
	kindRegex:         typeRegex,
	kindDBPointer:     typeDBPointer,
	kindJavaScript:    typeJavaScript,
	kindSymbol:        typeSymbol,
	kindCodeWithScope: typeCodeWithScope,
	kindTimestamp:     typeTimestamp,
	kindDecimal128:    typeDecimal128,
	kindMinKey:        typeMinKey,
	kindMaxKey:        typeMaxKey,
	kindUndefined:     typeUndefined,
	kindNull:          typeNull,
}

// decodeSpecial decodes into the codec's own value types and the stdlib types.
func decodeSpecial(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: a null or an undefined is the zero value of every one of them; the
	//: kinds before kindTime are not value types and are refused below.
	if (t == typeNull || t == typeUndefined) && p.kind >= kindTime {
		rv.SetZero()
		//: decoded.
		return nil
	}
	//: the dates and the stdlib types.
	if handled, err := decodeStdlib(t, val, rv, p, st); handled {
		//: decoded.
		return err
	}
	//: the BSON type a value type is read from.
	if want, ok := valueTypeBytes[p.kind]; ok && t == want {
		//: read as that type.
		return readValueType(t, val, rv, st)
	}
	//: the cross-type conversions the value types accept.
	return decodeValueTypeConversion(t, val, rv, p, st)
}

// decodeStdlib decodes into time.Time, url.URL and json.Number.
func decodeStdlib(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) (bool, error) {
	//: one rule per type.
	switch p.kind {
	case kindTime:
		return true, decodeTime(t, val, rv, p, st)
	case kindURL:
		return true, decodeURL(t, val, rv, p, st)
	case kindJSONNumber:
		return true, decodeJSONNumber(t, val, rv, p, st)
	default:
		//: none of them.
		return false, nil
	}
}

// decodeTime decodes a datetime, an int64 of milliseconds, a timestamp's
// seconds, or a string in timeStringLayout, into a UTC time.Time.
func decodeTime(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	var tm time.Time
	//: one rule per BSON type.
	switch t {
	case typeDateTime, typeInt64:
		tm = DateTime(readInt64(val)).Time()
	case typeTimestamp:
		tm = time.Unix(int64(readUint32(val[lengthSize:])), 0)
	case typeString:
		parsed, err := parseTimeString(val, st)
		//: not the layout.
		if err != nil {
			//: refused.
			return err
		}
		tm = parsed
	default:
		return mismatch(t, p, st)
	}
	setTyped(rv, tm.UTC())
	//: decoded.
	return nil
}

// parseTimeString parses a string value in timeStringLayout.
func parseTimeString(val []byte, st decodeState) (time.Time, error) {
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return time.Time{}, errCorrupt()
	}
	parsed, err := time.Parse(timeStringLayout, string(text))
	//: not the layout.
	if err != nil {
		//: refused, without quoting the text.
		return time.Time{}, unmarshalError(nil, inField("a string is not a time in the layout "+timeStringLayout, st))
	}
	//: the instant.
	return parsed, nil
}

// decodeURL decodes a string into a url.URL.
func decodeURL(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: only a string is a URL.
	if t != typeString {
		//: refused.
		return mismatch(t, p, st)
	}
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return errCorrupt()
	}
	u, err := url.Parse(string(text))
	//: not a URL.
	if err != nil {
		//: refused, without quoting the text.
		return unmarshalError(nil, inField("a string is not a URL", st))
	}
	setTyped(rv, *u)
	//: decoded.
	return nil
}

// decodeJSONNumber decodes a number into a json.Number, a double in its
// shortest decimal form.
func decodeJSONNumber(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: one rule per BSON type.
	switch t {
	case typeDouble:
		rv.SetString(strconv.FormatFloat(readDouble(val), 'f', -1, 64))
	case typeInt32:
		rv.SetString(strconv.FormatInt(int64(readInt32(val)), 10))
	case typeInt64:
		rv.SetString(strconv.FormatInt(readInt64(val), 10))
	default:
		return mismatch(t, p, st)
	}
	//: decoded.
	return nil
}

// readValueType sets rv, of the value type matching t, from val.
func readValueType(t byte, val []byte, rv reflect.Value, st decodeState) error {
	//: the two most common, without boxing.
	switch t {
	case typeObjectID:
		var id ObjectID
		copy(id[:], val)
		setTyped(rv, id)
		return nil
	case typeDateTime:
		rv.SetInt(readInt64(val))
		return nil
	}
	value, err := decodeAny(t, val, st)
	//: the value's own failure.
	if err != nil {
		//: refused.
		return err
	}
	//: the payload-free types are their zero value.
	if value == nil {
		rv.SetZero()
		//: decoded.
		return nil
	}
	rv.Set(reflect.ValueOf(value))
	//: decoded.
	return nil
}

// decodeValueTypeConversion takes the cross-type conversions the previous
// library accepted for its value types: a string into an ObjectID or a
// Symbol, a generic binary into a Symbol, null and undefined into each other.
func decodeValueTypeConversion(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: one rule per accepted pair.
	switch {
	case p.kind == kindObjectID && t == typeString:
		return decodeObjectIDString(val, rv, st)
	case p.kind == kindSymbol && t == typeString:
		return readValueType(typeSymbol, val, rv, st)
	case p.kind == kindSymbol && t == typeBinary:
		data, err := genericBinary(val, p, st)
		//: not a generic binary.
		if err != nil {
			//: refused.
			return err
		}
		rv.SetString(string(data))
		return nil
	default:
		//: anything else does not fit.
		return mismatch(t, p, st)
	}
}

// decodeObjectIDString decodes a string into an ObjectID: its 24-digit
// hexadecimal form, or exactly twelve bytes taken as they are, as the previous
// library accepted.
func decodeObjectIDString(val []byte, rv reflect.Value, st decodeState) error {
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return errCorrupt()
	}
	//: the hexadecimal form.
	if id, err := ObjectIDFromHex(string(text)); err == nil {
		setTyped(rv, id)
		//: decoded.
		return nil
	}
	//: twelve raw bytes.
	if len(text) != objectIDSize {
		//: refused.
		return unmarshalError(nil, inField("a string is neither 24 hexadecimal digits nor 12 bytes, so not an ObjectID", st))
	}
	var id ObjectID
	copy(id[:], text)
	setTyped(rv, id)
	//: decoded.
	return nil
}

// readUint32 reads the little-endian uint32 at the start of b.
func readUint32(b []byte) uint32 {
	//: the unsigned read.
	return uint32(readInt32(b))
}

// setTyped stores v in rv without boxing it when rv is addressable.
func setTyped[T any](rv reflect.Value, v T) {
	//: through the pointer.
	if rv.CanAddr() {
		//: the pointer's type is *T for every caller.
		if ptr, ok := reflect.TypeAssert[*T](rv.Addr()); ok {
			*ptr = v
			return
		}
	}
	rv.Set(reflect.ValueOf(v))
}
