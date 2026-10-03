// Package bson — decoding into scalar targets. The conversions accepted are
// the previous library's: a number decodes into any numeric target it fits, a
// boolean into a number and a number into a boolean, null and undefined into
// the zero value; anything else is refused rather than guessed.
package bson

import (
	"bytes"
	"encoding/hex"
	"math"
	"reflect"
	"strconv"
)

// twoTo63 is 2^63, the first double past the int64 range.
const twoTo63 float64 = 1 << 63

// decodeScalar decodes into the scalar mappings and reports whether p was one.
func decodeScalar(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) (bool, error) {
	//: one rule per scalar mapping.
	switch p.kind {
	case kindBool:
		return true, decodeBool(t, val, rv, p, st)
	case kindInt32, kindInt, kindInt64:
		return true, decodeSigned(t, val, rv, p, st)
	case kindUint16, kindUint64:
		return true, decodeUnsigned(t, val, rv, p, st)
	case kindFloat32, kindFloat64:
		return true, decodeFloat(t, val, rv, p, st)
	case kindString:
		return true, decodeString(t, val, rv, p, st)
	case kindBytes:
		return true, decodeBytes(t, val, rv, p, st)
	default:
		//: not a scalar.
		return false, nil
	}
}

// decodeBool decodes a boolean, a number (true when not zero), or a null.
func decodeBool(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: one rule per BSON type.
	switch t {
	case typeBoolean:
		rv.SetBool(val[0] == 1)
	case typeInt32:
		rv.SetBool(readInt32(val) != 0)
	case typeInt64:
		rv.SetBool(readInt64(val) != 0)
	case typeDouble:
		rv.SetBool(readDouble(val) != 0)
	case typeNull, typeUndefined:
		rv.SetBool(false)
	default:
		return mismatch(t, p, st)
	}
	//: decoded.
	return nil
}

// integerOf reads the integer a numeric, boolean or null value stands for. A
// double must be whole unless truncation was asked for, and within the int64
// range in either case.
func integerOf(t byte, val []byte, p *typePlan, st decodeState) (int64, error) {
	//: one rule per BSON type.
	switch t {
	case typeInt32:
		return int64(readInt32(val)), nil
	case typeInt64:
		return readInt64(val), nil
	case typeDouble:
		return wholeDouble(readDouble(val), p, st)
	case typeBoolean:
		return int64(val[0]), nil
	case typeNull, typeUndefined:
		return 0, nil
	default:
		//: anything else is not a number.
		return 0, mismatch(t, p, st)
	}
}

// wholeDouble converts a double to an integer: exactly, or dropping the
// fraction under truncate; never past the int64 range, never from NaN.
func wholeDouble(f float64, p *typePlan, st decodeState) (int64, error) {
	//: a fraction is kept unless truncation was asked for.
	if !st.truncate && math.Floor(f) != f {
		//: refused.
		return 0, unmarshalError(nil, inField("a double with a fraction cannot decode into "+p.typ.String()+" without the truncate option", st))
	}
	//: NaN, the infinities and the out-of-range values have no int64.
	if math.IsNaN(f) || f >= twoTo63 || f < -twoTo63 {
		//: refused.
		return 0, unmarshalError(nil, inField("a double is outside the range of "+p.typ.String(), st))
	}
	//: Go truncates toward zero.
	return int64(f), nil
}

// decodeSigned decodes into a signed integer kind, refusing a value it
// cannot hold.
func decodeSigned(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	i, err := integerOf(t, val, p, st)
	//: not a number.
	if err != nil {
		//: refused.
		return err
	}
	//: the kind's width.
	if rv.OverflowInt(i) {
		//: refused rather than wrapped.
		return unmarshalError(nil, inField("a value overflows "+p.typ.String(), st))
	}
	rv.SetInt(i)
	//: decoded.
	return nil
}

// decodeUnsigned decodes into an unsigned integer kind, refusing a negative
// value or one it cannot hold.
func decodeUnsigned(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	i, err := integerOf(t, val, p, st)
	//: not a number.
	if err != nil {
		//: refused.
		return err
	}
	//: negative, or past the kind's width.
	if i < 0 || rv.OverflowUint(uint64(i)) {
		//: refused rather than wrapped.
		return unmarshalError(nil, inField("a value overflows "+p.typ.String(), st))
	}
	rv.SetUint(uint64(i))
	//: decoded.
	return nil
}

// floatOf reads the number a numeric, boolean or null value stands for.
func floatOf(t byte, val []byte, p *typePlan, st decodeState) (float64, error) {
	//: one rule per BSON type.
	switch t {
	case typeDouble:
		return readDouble(val), nil
	case typeInt32:
		return float64(readInt32(val)), nil
	case typeInt64:
		return float64(readInt64(val)), nil
	case typeBoolean:
		return float64(val[0]), nil
	case typeNull, typeUndefined:
		return 0, nil
	default:
		//: anything else is not a number.
		return 0, mismatch(t, p, st)
	}
}

// decodeFloat decodes into a float kind. A double that a float32 would round
// is refused unless truncation was asked for; NaN is not refused for it.
func decodeFloat(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	f, err := floatOf(t, val, p, st)
	//: not a number.
	if err != nil {
		//: refused.
		return err
	}
	//: a float32 must hold the value exactly.
	if p.kind == kindFloat32 && !st.truncate && !math.IsNaN(f) && float64(float32(f)) != f {
		//: refused rather than rounded.
		return unmarshalError(nil, inField("a double does not fit "+p.typ.String()+" without the truncate option", st))
	}
	rv.SetFloat(f)
	//: decoded.
	return nil
}

// decodeString decodes a string, a symbol, an ObjectID (as its hexadecimal
// form), a generic binary (as its bytes) or a null into a string kind.
func decodeString(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: one rule per BSON type.
	switch t {
	case typeString, typeSymbol, typeBinary:
		data, err := textOf(t, val, p, st)
		//: not text, or not a generic binary.
		if err != nil {
			//: refused.
			return err
		}
		rv.SetString(string(data))
	case typeObjectID:
		rv.SetString(hex.EncodeToString(val[:objectIDSize]))
	case typeNull, typeUndefined:
		rv.SetString("")
	default:
		return mismatch(t, p, st)
	}
	//: decoded.
	return nil
}

// textOf returns the bytes of a string, a symbol or a generic binary.
func textOf(t byte, val []byte, p *typePlan, st decodeState) ([]byte, error) {
	//: a binary's payload, when its subtype is generic.
	if t == typeBinary {
		//: or its refusal.
		return genericBinary(val, p, st)
	}
	text, ok := stringPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	//: the text.
	return text, nil
}

// decodeBytes decodes a generic binary, a string or a symbol into a []byte, a
// null into nil. The bytes are copied.
func decodeBytes(t byte, val []byte, rv reflect.Value, p *typePlan, st decodeState) error {
	//: one rule per BSON type.
	switch t {
	case typeBinary, typeString, typeSymbol:
		data, err := textOf(t, val, p, st)
		//: not text, or not a generic binary.
		if err != nil {
			//: refused.
			return err
		}
		rv.SetBytes(bytes.Clone(data))
	case typeNull, typeUndefined:
		rv.SetZero()
	default:
		return mismatch(t, p, st)
	}
	//: decoded.
	return nil
}

// stringPayload returns the text of a string-shaped value, without its length
// and NUL.
func stringPayload(val []byte) ([]byte, bool) {
	//: the prefix and the NUL at least.
	if len(val) < lengthSize+1 {
		//: changed under us.
		return nil, false
	}
	//: between them.
	return val[lengthSize : len(val)-1], true
}

// binaryPayload returns a binary's subtype and bytes, the inner length of
// subtype 0x02 removed. The bytes alias val.
func binaryPayload(val []byte) (byte, []byte, bool) {
	//: the header at least.
	if len(val) < binaryHeaderSize {
		//: changed under us.
		return 0, nil, false
	}
	subtype, data := val[lengthSize], val[binaryHeaderSize:]
	//: the old generic subtype's inner length.
	if subtype == BinaryOld {
		//: the inner length must be there.
		if len(data) < lengthSize {
			//: changed under us.
			return 0, nil, false
		}
		data = data[lengthSize:]
	}
	//: the payload.
	return subtype, data, true
}

// genericBinary returns a binary's bytes when its subtype is one a byte
// target accepts — generic or old generic — and refuses any other.
func genericBinary(val []byte, p *typePlan, st decodeState) ([]byte, error) {
	subtype, data, ok := binaryPayload(val)
	//: changed under us.
	if !ok {
		//: refused.
		return nil, errCorrupt()
	}
	//: a UUID, a digest, an encrypted value… is not opaque bytes.
	if subtype != BinaryGeneric && subtype != BinaryOld {
		//: refused, naming the subtype.
		return nil, unmarshalError(nil, inField("a binary of subtype 0x"+strconv.FormatUint(uint64(subtype), 16)+" cannot decode into "+p.typ.String()+"; only 0x00 and 0x02 can", st))
	}
	//: the bytes.
	return data, nil
}
