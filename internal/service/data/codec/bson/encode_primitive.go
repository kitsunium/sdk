package bson

import (
	"encoding/binary"
	"math"
	"reflect"
	"time"
	"unicode/utf8"
)

// appendBool writes a boolean.
func (e *encoder) appendBool(b bool) {
	//: 0x01 for true.
	if b {
		e.buf = append(e.buf, 1)
		return
	}
	e.buf = append(e.buf, 0)
}

// appendInt32 writes an int32.
func (e *encoder) appendInt32(i int32) {
	e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(i))
}

// appendInt64 writes an int64.
func (e *encoder) appendInt64(i int64) {
	e.buf = binary.LittleEndian.AppendUint64(e.buf, uint64(i))
}

// appendInt writes i as an int32 when narrow allows it and it fits, as an
// int64 otherwise, and returns which.
func (e *encoder) appendInt(i int64, narrow bool) byte {
	//: the narrow form when asked for and possible.
	if narrow && i >= math.MinInt32 && i <= math.MaxInt32 {
		e.appendInt32(int32(i))
		//: an int32.
		return typeInt32
	}
	e.appendInt64(i)
	//: an int64.
	return typeInt64
}

// appendUint writes an unsigned integer: an int32 under minsize when it fits,
// an int64 when it fits there, refused past the int64 range.
func (e *encoder) appendUint(u uint64, minSize bool, p *typePlan) (byte, error) {
	//: the narrow form under minsize.
	if minSize && u <= math.MaxInt32 {
		e.appendInt32(int32(u))
		//: an int32.
		return typeInt32, nil
	}
	//: BSON has no unsigned 64-bit integer.
	if u > math.MaxInt64 {
		//: refused rather than wrapped to a negative.
		return 0, marshalError(nil, "a "+p.typ.String()+" value overflows int64, BSON's widest integer")
	}
	e.appendInt64(int64(u))
	//: an int64.
	return typeInt64, nil
}

// appendDouble writes an IEEE 754 double.
func (e *encoder) appendDouble(f float64) {
	e.buf = binary.LittleEndian.AppendUint64(e.buf, math.Float64bits(f))
}

// appendString writes a string value, refusing one that is not UTF-8.
func (e *encoder) appendString(s string) error {
	//: BSON strings are UTF-8, and a decode refuses anything else.
	if !utf8.ValidString(s) {
		//: refused rather than written undecodable.
		return marshalError(nil, "a string is not valid UTF-8")
	}
	e.appendStringBytes(s)
	//: written.
	return nil
}

// appendStringBytes writes the length, the bytes and the NUL of a string the
// caller has validated.
func (e *encoder) appendStringBytes(s string) {
	e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(len(s)+1))
	e.buf = append(e.buf, s...)
	e.buf = append(e.buf, 0)
}

// appendBinary writes a binary; the old generic subtype nests its length.
func (e *encoder) appendBinary(subtype byte, data []byte) {
	//: subtype 0x02 carries an inner length.
	if subtype == BinaryOld {
		e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(len(data)+lengthSize))
		e.buf = append(e.buf, subtype)
		e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(len(data)))
		e.buf = append(e.buf, data...)
		return
	}
	e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(len(data)))
	e.buf = append(e.buf, subtype)
	e.buf = append(e.buf, data...)
}

// appendByteArray writes a byte array as a generic binary.
func (e *encoder) appendByteArray(rv reflect.Value) {
	n := rv.Len()
	e.buf = binary.LittleEndian.AppendUint32(e.buf, uint32(n))
	e.buf = append(e.buf, BinaryGeneric)
	//: an addressable array is read in one copy.
	if rv.CanAddr() {
		e.buf = append(e.buf, rv.Bytes()...)
		return
	}
	//: otherwise byte by byte.
	for i := range n {
		e.buf = append(e.buf, byte(rv.Index(i).Uint()))
	}
}

// timeOf reads a time.Time without allocating.
func timeOf(rv reflect.Value) time.Time {
	tm, _ := reflect.TypeAssert[time.Time](rv)
	//: planned as a time.Time, so the assertion holds.
	return tm
}
