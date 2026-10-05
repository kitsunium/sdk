package cbor

import (
	"encoding/binary"
	"math"
)

// The eight major types, already shifted into the high three bits of an
// initial byte (RFC 8949 §3.1), so a head is `byte(major) | info`.
const (
	// majorUnsigned is major type 0: an unsigned integer.
	majorUnsigned majorType = iota << majorShift
	// majorNegative is major type 1: the negative integer −1−n.
	majorNegative
	// majorBytes is major type 2: a byte string.
	majorBytes
	// majorText is major type 3: a UTF-8 text string.
	majorText
	// majorArray is major type 4: an array of data items.
	majorArray
	// majorMap is major type 5: a map of pairs of data items.
	majorMap
	// majorTag is major type 6: a tag number and the data item it encloses.
	majorTag
	// majorSimple is major type 7: simple values and floating-point numbers.
	majorSimple
)

// The layout of an initial byte and the meaning of its low five bits.
const (
	// majorShift is where the major type sits in an initial byte.
	majorShift uint = 5
	// majorMask selects the major type of an initial byte.
	majorMask byte = 0xe0
	// infoMask selects the additional information of an initial byte.
	infoMask byte = 0x1f
	// infoDirectMax is the largest argument the initial byte carries itself.
	infoDirectMax byte = 23
	// info1Byte announces a one-byte argument (and, in major type 7, a
	// simple value in the extension byte).
	info1Byte byte = 24
	// info2Bytes announces a two-byte argument, or a half-precision float.
	info2Bytes byte = 25
	// info4Bytes announces a four-byte argument, or a single-precision float.
	info4Bytes byte = 26
	// info8Bytes announces an eight-byte argument, or a double-precision float.
	info8Bytes byte = 27
	// infoReservedMin is the first of the three values RFC 8949 §3 reserves:
	// an item using 28, 29 or 30 is not well-formed.
	infoReservedMin byte = 28
	// infoReservedMax is the last reserved additional information value.
	infoReservedMax byte = 30
	// infoIndefinite is an indefinite length in major types 2 to 5, and the
	// "break" stop code in major type 7.
	infoIndefinite byte = 31
)

// The four simple values RFC 8949 §3.3 assigns, consecutive from 20.
const (
	// simpleFalse is the simple value false.
	simpleFalse byte = iota + firstAssignedSimple
	// simpleTrue is the simple value true.
	simpleTrue
	// simpleNull is the simple value null.
	simpleNull
	// simpleUndefined is the simple value undefined, which decodes as null.
	simpleUndefined
)

// The bounds around the assigned simple values.
const (
	// firstAssignedSimple is the first simple value RFC 8949 assigns, false.
	firstAssignedSimple byte = 20
	// minExtendedSimple is the smallest simple value the one-byte extension
	// may carry: below it the two-byte form is not well-formed.
	minExtendedSimple uint64 = 32
)

// Single bytes and three-byte floats the encoder writes verbatim.
const (
	// nullByte is the encoded null.
	nullByte = byte(majorSimple) | simpleNull
	// falseByte is the encoded false.
	falseByte = byte(majorSimple) | simpleFalse
	// trueByte is the encoded true.
	trueByte = byte(majorSimple) | simpleTrue
	// breakByte is the stop code closing an indefinite-length item.
	breakByte = byte(majorSimple) | infoIndefinite
	// float16Head opens a half-precision float.
	float16Head = byte(majorSimple) | info2Bytes
	// float32Head opens a single-precision float.
	float32Head = byte(majorSimple) | info4Bytes
	// float64Head opens a double-precision float.
	float64Head = byte(majorSimple) | info8Bytes
	// halfNaN is the one NaN the encoder writes, 0xf97e00 (RFC 8949 §4.2.2).
	halfNaN uint16 = 0x7e00
	// halfPosInf is +Infinity as a half-precision float.
	halfPosInf uint16 = 0x7c00
	// halfNegInf is −Infinity as a half-precision float.
	halfNegInf uint16 = 0xfc00
)

// The tag numbers this codec interprets; every other tag is transparent.
const (
	// tagDateTime encloses an RFC 3339 date/time text string (RFC 8949 §3.4.1).
	tagDateTime uint64 = iota
	// tagEpoch encloses an epoch-based date/time number (RFC 8949 §3.4.2).
	tagEpoch
	// tagPositiveBignum encloses the magnitude n of a bignum (RFC 8949 §3.4.3).
	tagPositiveBignum
	// tagNegativeBignum encloses the n of the negative bignum −1−n.
	tagNegativeBignum
)

// The IEEE 754 binary16 and binary64 layouts the half-precision decoder maps
// between (RFC 8949 Appendix D).
const (
	// halfSignBit selects the sign of a binary16.
	halfSignBit uint16 = 0x8000
	// halfMantissaBits is the width of a binary16 significand.
	halfMantissaBits uint = 10
	// halfExponentMask selects the five exponent bits once shifted down.
	halfExponentMask uint16 = 0x1f
	// halfMantissaMask selects the ten significand bits of a binary16.
	halfMantissaMask uint16 = 0x3ff
	// halfExponentBias is the binary16 exponent bias.
	halfExponentBias uint64 = 15
	// halfSubnormalExponent scales a subnormal binary16 significand: 2^−24.
	halfSubnormalExponent int = -24
	// doubleExponentBias is the binary64 exponent bias.
	doubleExponentBias uint64 = 1023
	// doubleMantissaBits is the width of a binary64 significand.
	doubleMantissaBits uint = 52
	// doubleExponentAllOnes is the binary64 exponent of infinities and NaNs.
	doubleExponentAllOnes uint64 = 0x7ff
	// signShiftHalfToDouble moves the binary16 sign bit to the binary64 one.
	signShiftHalfToDouble uint = 48
	// mantissaShiftHalfToDouble left-aligns a binary16 significand in a
	// binary64 one, which keeps a NaN payload and its quiet bit.
	mantissaShiftHalfToDouble = doubleMantissaBits - halfMantissaBits
)

// majorType is the high three bits of an initial byte (RFC 8949 §3.1).
type majorType byte

// itemHead is one decoded head: the initial byte and its argument.
type itemHead struct {
	// arg is the argument — a value, a length, a count or a tag number. For
	// additional information 0..23 it is the additional information itself.
	arg uint64
	// size is how many bytes the head occupies, the initial byte included.
	size int
	// major is the major type.
	major majorType
	// info is the additional information.
	info byte
}

// appendHead appends the shortest head for major and the argument n — the
// preferred serialization of RFC 8949 §4.1, which every integer, length,
// count and tag number this encoder writes uses.
func appendHead(b []byte, major majorType, n uint64) []byte {
	initial := byte(major)
	//: the argument picks the narrowest of the five head widths.
	switch {
	case n <= uint64(infoDirectMax):
		//: carried in the initial byte itself.
		return append(b, initial|byte(n))
	case n <= math.MaxUint8:
		//: one extension byte.
		return append(b, initial|info1Byte, byte(n))
	case n <= math.MaxUint16:
		//: two extension bytes, network byte order.
		return binary.BigEndian.AppendUint16(append(b, initial|info2Bytes), uint16(n))
	case n <= math.MaxUint32:
		//: four extension bytes.
		return binary.BigEndian.AppendUint32(append(b, initial|info4Bytes), uint32(n))
	default:
		//: eight extension bytes.
		return binary.BigEndian.AppendUint64(append(b, initial|info8Bytes), n)
	}
}

// readHead decodes the head starting at data[off]. ok is false when data
// ends before the head does. Additional information 28 to 31 carries no
// argument and is returned as read, for the caller to judge.
func readHead(data []byte, off int) (h itemHead, ok bool) {
	//: not even an initial byte.
	if off >= len(data) {
		//: the caller needs more input.
		return itemHead{}, false
	}
	initial := data[off]
	h = itemHead{major: majorType(initial & majorMask), info: initial & infoMask, size: 1}
	//: additional information 0..23 is its own argument.
	if h.info <= infoDirectMax {
		h.arg = uint64(h.info)
		//: a one-byte head.
		return h, true
	}
	width := argumentWidth(h.info)
	//: 28..31 carry no argument at all.
	if width == 0 {
		//: a one-byte head, judged by the caller.
		return h, true
	}
	//: the extension bytes must all be present.
	if len(data)-off-1 < width {
		//: the head is cut short.
		return itemHead{}, false
	}
	h.arg = readUint(data[off+1:], h.info)
	h.size += width
	//: a complete head.
	return h, true
}

// argumentWidth is how many extension bytes the additional information
// info announces: 1, 2, 4 or 8 for 24..27 and none otherwise.
func argumentWidth(info byte) int {
	//: only 24..27 have extension bytes.
	if info < info1Byte || info > info8Bytes {
		//: none.
		return 0
	}
	//: 24 → 1, 25 → 2, 26 → 4, 27 → 8.
	return 1 << (info - info1Byte)
}

// readUint reads the big-endian argument the additional information info
// announces from the start of b, which the caller has checked is long enough.
func readUint(b []byte, info byte) uint64 {
	//: one branch per argument width.
	switch info {
	case info1Byte:
		//: one byte.
		return uint64(b[0])
	case info2Bytes:
		//: two bytes.
		return uint64(binary.BigEndian.Uint16(b))
	case info4Bytes:
		//: four bytes.
		return uint64(binary.BigEndian.Uint32(b))
	default:
		//: eight bytes.
		return binary.BigEndian.Uint64(b)
	}
}

// halfToFloat64 widens an IEEE 754 binary16 to a float64 exactly. A NaN keeps
// its payload and quiet bit; signed zeros and subnormals keep their sign.
func halfToFloat64(half uint16) float64 {
	sign := uint64(half&halfSignBit) << signShiftHalfToDouble
	exponent := (half >> halfMantissaBits) & halfExponentMask
	mantissa := uint64(half & halfMantissaMask)
	//: the exponent decides between subnormal, special and normal.
	switch exponent {
	case 0:
		//: zero or subnormal: the significand scaled by 2^−24, then signed.
		magnitude := math.Ldexp(float64(mantissa), halfSubnormalExponent)
		//: setting the sign bit keeps −0 negative.
		return math.Float64frombits(math.Float64bits(magnitude) | sign)
	case halfExponentMask:
		//: infinity when the significand is zero, NaN (payload kept) otherwise.
		return math.Float64frombits(sign | doubleExponentAllOnes<<doubleMantissaBits | mantissa<<mantissaShiftHalfToDouble)
	default:
		//: a normal number: rebias the exponent, left-align the significand.
		rebiased := uint64(exponent) - halfExponentBias + doubleExponentBias
		//: exact — every binary16 is a binary64.
		return math.Float64frombits(sign | rebiased<<doubleMantissaBits | mantissa<<mantissaShiftHalfToDouble)
	}
}
