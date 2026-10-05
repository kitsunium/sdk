package bson

import (
	"bytes"
	"encoding/binary"
	"math"
)

// Element type bytes, BSON 1.1. The deprecated types (undefined, DBPointer,
// symbol, code with scope) are still read and written: a document an older
// tool produced decodes, and re-encodes to the same bytes.
const (
	// typeDouble is a 64-bit IEEE 754 binary floating point.
	typeDouble byte = 0x01
	// typeString is an int32-length-prefixed, NUL-terminated UTF-8 string.
	typeString byte = 0x02
	// typeDocument is an embedded document.
	typeDocument byte = 0x03
	// typeArray is a document whose keys are "0", "1", ….
	typeArray byte = 0x04
	// typeBinary is an int32 length, a subtype byte and the bytes.
	typeBinary byte = 0x05
	// typeUndefined is the deprecated undefined value: no payload.
	typeUndefined byte = 0x06
	// typeObjectID is twelve bytes.
	typeObjectID byte = 0x07
	// typeBoolean is one byte, 0x00 or 0x01 and nothing else.
	typeBoolean byte = 0x08
	// typeDateTime is milliseconds since the Unix epoch, UTC, as an int64.
	typeDateTime byte = 0x09
	// typeNull is null: no payload.
	typeNull byte = 0x0A
	// typeRegex is two cstrings: the pattern, then the options.
	typeRegex byte = 0x0B
	// typeDBPointer is the deprecated DBPointer: a string and twelve bytes.
	typeDBPointer byte = 0x0C
	// typeJavaScript is JavaScript code, laid out as a string.
	typeJavaScript byte = 0x0D
	// typeSymbol is the deprecated symbol, laid out as a string.
	typeSymbol byte = 0x0E
	// typeCodeWithScope is an int32 total length, a string and a document.
	typeCodeWithScope byte = 0x0F
	// typeInt32 is a 32-bit signed integer.
	typeInt32 byte = 0x10
	// typeTimestamp is the replication timestamp: increment, then seconds.
	typeTimestamp byte = 0x11
	// typeInt64 is a 64-bit signed integer.
	typeInt64 byte = 0x12
	// typeDecimal128 is an IEEE 754-2008 decimal128 in BID encoding.
	typeDecimal128 byte = 0x13
	// typeMinKey compares lower than every other value: no payload.
	typeMinKey byte = 0xFF
	// typeMaxKey compares higher than every other value: no payload.
	typeMaxKey byte = 0x7F
)

// Binary subtypes, BSON 1.1. A subtype is a single byte the codec carries
// through unchanged; only BinaryGeneric and BinaryOld decode into []byte.
const (
	// BinaryGeneric is the default subtype, and the one a []byte is written as.
	BinaryGeneric byte = 0x00
	// BinaryFunction marks a function.
	BinaryFunction byte = 0x01
	// BinaryOld is the deprecated generic subtype: its bytes are themselves an
	// int32 length followed by that many bytes, and the codec writes and checks
	// that inner length.
	BinaryOld byte = 0x02
	// BinaryUUIDOld is the deprecated, byte-order-ambiguous UUID subtype.
	BinaryUUIDOld byte = 0x03
	// BinaryUUID is an RFC 9562 UUID.
	BinaryUUID byte = 0x04
	// BinaryMD5 is an MD5 digest.
	BinaryMD5 byte = 0x05
	// BinaryEncrypted is a client-side encrypted value.
	BinaryEncrypted byte = 0x06
	// BinaryColumn is a compressed time-series column.
	BinaryColumn byte = 0x07
	// BinarySensitive marks data a server must not log.
	BinarySensitive byte = 0x08
	// BinaryVector is a packed numeric vector.
	BinaryVector byte = 0x09
	// BinaryUserDefined is the first of the subtypes reserved for applications.
	BinaryUserDefined byte = 0x80
)

// Structural bounds.
const (
	// maxBSONNestedLevels bounds how deeply documents and arrays nest, the
	// top-level document counting as the first level: the depth MongoDB
	// accepts for a stored document. Both directions enforce it — a decode
	// before it recurses (CWE-674), an encode so that a cyclic value is refused
	// instead of overflowing the stack.
	maxBSONNestedLevels int = 100
	// maxIndirections bounds how many pointers and interfaces an encode follows
	// in a row without reaching a value: a pointer that points at itself adds no
	// nesting level, so the depth bound alone would never stop it.
	maxIndirections int = 64
	// minDocumentSize is the smallest document: the int32 length and the
	// terminating NUL.
	minDocumentSize int = 5
	// lengthSize is the width of every int32 length prefix.
	lengthSize int = 4
	// objectIDSize is the width of an ObjectID.
	objectIDSize int = 12
	// decimal128Size is the width of a decimal128.
	decimal128Size int = 16
	// binaryHeaderSize is a binary's int32 length plus its subtype byte.
	binaryHeaderSize int = 5
	// minCodeWithScopeSize is a code-with-scope's total length, its code
	// string's length and NUL, and the smallest scope document.
	minCodeWithScopeSize int = 14
	// wordSize is the width of a double, a datetime, a timestamp and an int64.
	wordSize int = 8
	// typeByteValues is how many values a type byte can take.
	typeByteValues int = 256
)

// Tables shared by the readers and the writer.
var (
	// emptyDocument is the five bytes of the empty document: its length, then
	// its terminator.
	emptyDocument = [minDocumentSize]byte{byte(minDocumentSize), 0, 0, 0, 0}
	// fixedWidths holds, per type byte, one more than the width of its value
	// when that width is fixed, and zero for a variable-width or unknown type.
	fixedWidths = [typeByteValues]uint8{
		typeUndefined:  1,
		typeNull:       1,
		typeMinKey:     1,
		typeMaxKey:     1,
		typeBoolean:    2,
		typeInt32:      uint8(lengthSize + 1),
		typeDouble:     uint8(wordSize + 1),
		typeDateTime:   uint8(wordSize + 1),
		typeTimestamp:  uint8(wordSize + 1),
		typeInt64:      uint8(wordSize + 1),
		typeObjectID:   uint8(objectIDSize + 1),
		typeDecimal128: uint8(decimal128Size + 1),
	}
)

// readInt32 reads the little-endian int32 at the start of b, which the caller
// has checked holds at least lengthSize bytes.
func readInt32(b []byte) int32 {
	//: two's complement reinterpretation of the unsigned read.
	return int32(binary.LittleEndian.Uint32(b))
}

// readInt64 reads the little-endian int64 at the start of b, which the caller
// has checked holds at least eight bytes.
func readInt64(b []byte) int64 {
	//: two's complement reinterpretation of the unsigned read.
	return int64(binary.LittleEndian.Uint64(b))
}

// readDouble reads the little-endian IEEE 754 double at the start of b.
func readDouble(b []byte) float64 {
	//: the bit pattern is the wire value.
	return math.Float64frombits(binary.LittleEndian.Uint64(b))
}

// cstringEnd returns the index of the NUL that ends the cstring at the start
// of b, or -1 when b holds no NUL.
func cstringEnd(b []byte) int {
	//: the stdlib scan is vectorised.
	return bytes.IndexByte(b, 0)
}

// valueSize returns how many bytes the value of an element of type t occupies
// at the start of b, or -1 when b cannot hold it or t is not a BSON type. It
// checks bounds only: the validator has already checked the content, and this
// is how the decoder steps over a value without reading it.
func valueSize(t byte, b []byte) int {
	//: the fixed-width types first.
	if size, fixed := fixedValueSize(t); fixed {
		//: the bytes must be there.
		if size > len(b) {
			//: truncated.
			return -1
		}
		//: the width of the type.
		return size
	}
	//: the variable-width types.
	return variableValueSize(t, b)
}

// fixedValueSize returns the width of a fixed-width type, and false for a
// variable-width or unknown one.
func fixedValueSize(t byte) (size int, fixed bool) {
	width := fixedWidths[t]
	//: zero marks a type whose width is not fixed.
	if width == 0 {
		//: not fixed-width.
		return 0, false
	}
	//: the table stores one more than the width.
	return int(width) - 1, true
}

// variableValueSize returns the width of a length-prefixed or cstring value,
// or -1 when b cannot hold it or t is not a BSON type.
func variableValueSize(t byte, b []byte) int {
	//: one layout per type byte.
	switch t {
	//: int32 length of the bytes that follow it, NUL included.
	case typeString, typeJavaScript, typeSymbol:
		return prefixedSize(b, lengthSize)
	//: int32 length of the whole value, the prefix included.
	case typeDocument, typeArray, typeCodeWithScope:
		return prefixedSize(b, 0)
	//: int32 length of the bytes after the subtype byte.
	case typeBinary:
		return prefixedSize(b, binaryHeaderSize)
	//: a string, then an ObjectID.
	case typeDBPointer:
		return dbPointerSize(b)
	//: two cstrings.
	case typeRegex:
		return regexSize(b)
	}
	//: not a BSON type.
	return -1
}

// prefixedSize returns extra plus the int32 length at the start of b, or -1
// when b is too short for the prefix or for the value it announces.
func prefixedSize(b []byte, extra int) int {
	//: the prefix itself must be there.
	if len(b) < lengthSize {
		//: truncated.
		return -1
	}
	declared := readInt32(b)
	//: negative, or past the bytes there are; compared without adding, so a
	//: 32-bit int cannot overflow.
	if declared < 0 || int(declared) > len(b)-extra {
		//: malformed.
		return -1
	}
	//: the whole value.
	return int(declared) + extra
}

// dbPointerSize returns the width of a DBPointer: a string, then an ObjectID.
func dbPointerSize(b []byte) int {
	str := prefixedSize(b, lengthSize)
	//: the namespace string must be readable.
	if str < 0 || len(b)-str < objectIDSize {
		//: truncated.
		return -1
	}
	//: the string and the twelve bytes after it.
	return str + objectIDSize
}

// regexSize returns the width of a regex: two NUL-terminated cstrings.
func regexSize(b []byte) int {
	pattern := cstringEnd(b)
	//: the pattern must be terminated.
	if pattern < 0 {
		//: truncated.
		return -1
	}
	options := cstringEnd(b[pattern+1:])
	//: the options must be terminated.
	if options < 0 {
		//: truncated.
		return -1
	}
	//: both strings and both NULs.
	return pattern + 1 + options + 1
}
