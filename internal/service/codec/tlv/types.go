// Package tlv — wire-format tag constants and shared limits for the TLV
// (Type-Length-Value) codec.
//
// Wire layout (each record):
//
//	tag(1B) length(varint) value(...)
//
// length is unsigned LEB128 (1-10 bytes); value layout depends on the tag.
package tlv

// Numeric limits — grouped at the top so const → type → var → func
// ordering rules are satisfied across the file.
const (
	// maxTLVBytes caps the Unmarshal input size for untrusted payloads.
	// 10 MiB matches the msgpack/ndjson scanner caps and shields the
	// decoder from CWE-400 memory-exhaustion pre-allocation attacks.
	maxTLVBytes int = 10 << 20

	// maxTLVDepth caps the nesting depth at 32 levels (CWE-674 defence).
	maxTLVDepth int = 32

	// maxFieldNameBytes caps the byte length of a struct field name on
	// the wire. Mirrors the LDAP/X.509-style 255-byte attribute cap.
	maxFieldNameBytes int = 255

	// maxVarintBytes is the worst-case LEB128 length for a uint64.
	maxVarintBytes int = 10

	// sliceHintCap clamps an attacker-declared length to a sane initial
	// capacity hint so a malformed buffer cannot trigger a multi-million
	// entry pre-allocation. The decode loop still grows on demand.
	sliceHintCap int = 4096

	// width8 — 1-byte payload (int8 / uint8).
	width8 int = 1
	// width16 — 2-byte payload (int16 / uint16).
	width16 int = 2
	// width32 — 4-byte payload (int32 / uint32 / float32).
	width32 int = 4
	// width64 — 8-byte payload (int64 / uint64 / float64).
	width64 int = 8

	// minRecordBytes is the smallest legal record (tag + 1-byte length).
	minRecordBytes int = 2

	// varintContinuationBit identifies the high bit in a LEB128 byte.
	varintContinuationBit byte = 0x80

	// varintPayloadMask isolates the 7-bit payload of an LEB128 byte.
	varintPayloadMask byte = 0x7f

	// varintShiftStep is the bit-shift per LEB128 byte (7 bits each).
	varintShiftStep uint = 7

	// varintMaxLastByte is the largest legal final byte in a 10-byte
	// uint64 varint (the MSB pair-of-bits cannot exceed 1).
	varintMaxLastByte byte = 1

	// decimalBase is the base used to format diagnostic numbers.
	decimalBase int = 10

	// mapPairStride is the number of slice slots per map pair in the
	// flattened (key, value, …) buffer — two: one for the key, one for
	// the value.
	mapPairStride int = 2
)

// Wire-format tag constants — assigned in stable byte ranges grouped by
// family so adding new types (e.g. 0x14 Int128) does not perturb the
// existing allocation table.
const (
	// tagNil represents a typed nil; length is always zero.
	tagNil Tag = 0x01
	// tagBoolFalse represents the boolean value false; length zero.
	tagBoolFalse Tag = 0x02
	// tagBoolTrue represents the boolean value true; length zero.
	tagBoolTrue Tag = 0x03

	// tagInt8 — signed 8-bit integer (1 byte big-endian).
	tagInt8 Tag = 0x10
	// tagInt16 — signed 16-bit integer (2 bytes big-endian).
	tagInt16 Tag = 0x11
	// tagInt32 — signed 32-bit integer (4 bytes big-endian).
	tagInt32 Tag = 0x12
	// tagInt64 — signed 64-bit integer (8 bytes big-endian).
	tagInt64 Tag = 0x13

	// tagUint8 — unsigned 8-bit integer (1 byte).
	tagUint8 Tag = 0x20
	// tagUint16 — unsigned 16-bit integer (2 bytes big-endian).
	tagUint16 Tag = 0x21
	// tagUint32 — unsigned 32-bit integer (4 bytes big-endian).
	tagUint32 Tag = 0x22
	// tagUint64 — unsigned 64-bit integer (8 bytes big-endian).
	tagUint64 Tag = 0x23

	// tagFloat32 — IEEE 754 single precision (4 bytes big-endian).
	tagFloat32 Tag = 0x30
	// tagFloat64 — IEEE 754 double precision (8 bytes big-endian).
	tagFloat64 Tag = 0x31

	// tagString — UTF-8 string; length = byte count of the payload.
	tagString Tag = 0x40
	// tagBytes — opaque byte sequence; length = byte count.
	tagBytes Tag = 0x41

	// tagSlice — heterogeneous sequence; length = element count, each
	// element is itself a full TLV record.
	tagSlice Tag = 0x50

	// tagMap — key-value pairs; length = pair count, each pair = two
	// adjacent TLV records (key then value).
	tagMap Tag = 0x60

	// tagStruct — named fields; length = field count, each field = a
	// string-tag TLV holding the field name followed by a value TLV.
	tagStruct Tag = 0x70
)

// Tag is the 1-byte type discriminator that opens every TLV record.
type Tag uint8
