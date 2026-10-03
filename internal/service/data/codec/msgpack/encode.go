// Package msgpack — the encode primitives. Each one appends one MessagePack
// item to a byte slice and chooses the SHORTEST form the value fits, the
// choice the vendor-backed codec made with UseCompactInts: a non-negative
// integer of any Go type is a positive fixint or a uint 8/16/32/64, a negative
// one a negative fixint or an int 8/16/32/64, so the Go width of a number is
// not on the wire and two programs that disagree about it still interoperate.
// Floats keep their width: float32 is float 32 and float64 is float 64.
package msgpack

import (
	"encoding/binary"
	"math"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// lengthForms lists, for one length-prefixed family, the header byte of each
// width and the largest length its fix form holds.
type lengthForms struct {
	// fixMax is the largest length the fix form holds, or noFixForm.
	fixMax int
	// fixPrefix is the fix form's prefix, ORed with the length.
	fixPrefix byte
	// code8 is the 8-bit-length form, or 0 when the family has none.
	code8 byte
	// code16 is the 16-bit-length form.
	code16 byte
	// code32 is the 32-bit-length form.
	code32 byte
}

// The four length-prefixed families.
var (
	// strForms are the forms of str: fixstr, str 8, str 16, str 32.
	strForms = lengthForms{fixMax: fixstrMax, fixPrefix: fixstrPrefix, code8: codeStr8, code16: codeStr16, code32: codeStr32}
	// binForms are the forms of bin: bin 8, bin 16, bin 32.
	binForms = lengthForms{fixMax: noFixForm, code8: codeBin8, code16: codeBin16, code32: codeBin32}
	// arrayForms are the forms of array: fixarray, array 16, array 32.
	arrayForms = lengthForms{fixMax: fixCountMax, fixPrefix: fixarrayPrefix, code16: codeArray16, code32: codeArray32}
	// mapForms are the forms of map: fixmap, map 16, map 32.
	mapForms = lengthForms{fixMax: fixCountMax, fixPrefix: fixmapPrefix, code16: codeMap16, code32: codeMap32}
)

// appendNil appends nil.
func appendNil(b []byte) []byte {
	//: one byte, no payload.
	return append(b, codeNil)
}

// appendBool appends true or false.
func appendBool(b []byte, v bool) []byte {
	//: true and false are distinct bytes.
	if v {
		//: true.
		return append(b, codeTrue)
	}
	//: false.
	return append(b, codeFalse)
}

// appendUint appends n in the shortest unsigned form it fits.
func appendUint(b []byte, n uint64) []byte {
	//: smallest form first.
	switch {
	//: 0–127 is a positive fixint: the byte is the value.
	case n <= uint64(posFixintMax):
		return append(b, byte(n))
	//: uint 8.
	case n <= math.MaxUint8:
		return append(b, codeUint8, byte(n))
	//: uint 16.
	case n <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(b, codeUint16), uint16(n))
	//: uint 32.
	case n <= math.MaxUint32:
		return binary.BigEndian.AppendUint32(append(b, codeUint32), uint32(n))
	//: uint 64.
	default:
		return binary.BigEndian.AppendUint64(append(b, codeUint64), n)
	}
}

// appendInt appends n in the shortest form it fits: a non-negative value is
// written as unsigned, exactly as the vendor-backed codec wrote it.
func appendInt(b []byte, n int64) []byte {
	//: the sign decides the family.
	switch {
	//: zero and positive values use the unsigned forms.
	case n >= 0:
		return appendUint(b, uint64(n))
	//: −32…−1 is a negative fixint: the byte is the value.
	case n >= negFixintFloor:
		return append(b, byte(n))
	//: int 8.
	case n >= math.MinInt8:
		return append(b, codeInt8, byte(n))
	//: int 16.
	case n >= math.MinInt16:
		return binary.BigEndian.AppendUint16(append(b, codeInt16), uint16(n))
	//: int 32.
	case n >= math.MinInt32:
		return binary.BigEndian.AppendUint32(append(b, codeInt32), uint32(n))
	//: int 64.
	default:
		return binary.BigEndian.AppendUint64(append(b, codeInt64), uint64(n))
	}
}

// appendFloat32 appends a float 32, bit for bit.
func appendFloat32(b []byte, f float32) []byte {
	//: IEEE 754 single precision, big-endian.
	return binary.BigEndian.AppendUint32(append(b, codeFloat32), math.Float32bits(f))
}

// appendFloat64 appends a float 64, bit for bit.
func appendFloat64(b []byte, f float64) []byte {
	//: IEEE 754 double precision, big-endian.
	return binary.BigEndian.AppendUint64(append(b, codeFloat64), math.Float64bits(f))
}

// appendLength appends the header of a length-prefixed item in its shortest
// form. The caller has checked n with checkLength.
func appendLength(b []byte, n int, forms *lengthForms) []byte {
	//: smallest form first.
	switch {
	//: the fix form carries the length in the byte.
	case n <= forms.fixMax:
		return append(b, forms.fixPrefix|byte(n))
	//: the 8-bit form, for the families that have one.
	case n <= math.MaxUint8 && forms.code8 != 0:
		return append(b, forms.code8, byte(n))
	//: the 16-bit form.
	case n <= math.MaxUint16:
		return binary.BigEndian.AppendUint16(append(b, forms.code16), uint16(n))
	//: the 32-bit form.
	default:
		return binary.BigEndian.AppendUint32(append(b, forms.code32), uint32(n))
	}
}

// checkLength refuses a length MessagePack cannot represent: every length
// field is at most 32 bits.
func checkLength(n int) error {
	//: a 32-bit length field holds up to 4 GiB − 1.
	if uint64(n) > math.MaxUint32 {
		//: name the length, never the content.
		return marshalFault("length exceeds what a MessagePack length field holds", errs.Int(fieldLen, n))
	}
	//: representable.
	return nil
}

// appendString appends s as a str.
func appendString(b []byte, s string) ([]byte, error) {
	//: refuse a length the wire cannot carry.
	if err := checkLength(len(s)); err != nil {
		//: leave b as it was.
		return b, err
	}
	//: header, then the bytes as they are — a Go string is not validated as
	//: UTF-8 here, so every Go string round-trips byte for byte.
	return append(appendLength(b, len(s), &strForms), s...), nil
}

// appendBinary appends p as a bin.
func appendBinary(b, p []byte) ([]byte, error) {
	//: refuse a length the wire cannot carry.
	if err := checkLength(len(p)); err != nil {
		//: leave b as it was.
		return b, err
	}
	//: header, then the bytes.
	return append(appendLength(b, len(p), &binForms), p...), nil
}

// appendBytes appends p as a bin, or nil when p is a nil slice — the
// distinction the vendor-backed codec drew, kept so a nil []byte and an empty
// one round-trip as themselves.
func appendBytes(b, p []byte) ([]byte, error) {
	//: a nil slice is nil on the wire.
	if p == nil {
		//: nil.
		return appendNil(b), nil
	}
	//: an empty or populated slice is a bin.
	return appendBinary(b, p)
}

// appendArrayHeader appends the header of an array of n elements.
func appendArrayHeader(b []byte, n int) ([]byte, error) {
	//: refuse a count the wire cannot carry.
	if err := checkLength(n); err != nil {
		//: leave b as it was.
		return b, err
	}
	//: shortest array form.
	return appendLength(b, n, &arrayForms), nil
}

// appendMapHeader appends the header of a map of n pairs.
func appendMapHeader(b []byte, n int) ([]byte, error) {
	//: refuse a count the wire cannot carry.
	if err := checkLength(n); err != nil {
		//: leave b as it was.
		return b, err
	}
	//: shortest map form.
	return appendLength(b, n, &mapForms), nil
}
