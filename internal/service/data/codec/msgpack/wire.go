package msgpack

// Fix-form prefixes, masks and bounds (spec.md §Formats). A fix form carries
// its value or length inside the header byte itself.
const (
	// posFixintMax is the largest positive fixint, 0xxxxxxx.
	posFixintMax byte = 0x7f
	// fixmapPrefix is the high nibble of a fixmap, 1000xxxx.
	fixmapPrefix byte = 0x80
	// fixarrayPrefix is the high nibble of a fixarray, 1001xxxx.
	fixarrayPrefix byte = 0x90
	// fixstrPrefix is the high three bits of a fixstr, 101xxxxx.
	fixstrPrefix byte = 0xa0
	// negFixintMin is the first negative fixint, 111xxxxx.
	negFixintMin byte = 0xe0
	// fixCountMask extracts a fixmap's or fixarray's element count.
	fixCountMask byte = 0x0f
	// fixstrLenMask extracts a fixstr's byte length.
	fixstrLenMask byte = 0x1f
	// fixCountMax is the largest count a fixmap or fixarray can carry.
	fixCountMax int = 15
	// fixstrMax is the largest byte length a fixstr can carry.
	fixstrMax int = 31
	// noFixForm marks a family (bin) that has no fix form at all.
	noFixForm int = -1
	// negFixintFloor is the smallest value a negative fixint can carry.
	negFixintFloor int64 = -32
)

// Format bytes of the 0xc0–0xdf block, in specification order.
const (
	// codeNil is nil.
	codeNil byte = 0xc0
	// codeFalse is false.
	codeFalse byte = 0xc2
	// codeTrue is true.
	codeTrue byte = 0xc3
	// codeBin8 is bin 8.
	codeBin8 byte = 0xc4
	// codeBin16 is bin 16.
	codeBin16 byte = 0xc5
	// codeBin32 is bin 32.
	codeBin32 byte = 0xc6
	// codeExt8 is ext 8.
	codeExt8 byte = 0xc7
	// codeFloat32 is float 32.
	codeFloat32 byte = 0xca
	// codeFloat64 is float 64.
	codeFloat64 byte = 0xcb
	// codeUint8 is uint 8.
	codeUint8 byte = 0xcc
	// codeUint16 is uint 16.
	codeUint16 byte = 0xcd
	// codeUint32 is uint 32.
	codeUint32 byte = 0xce
	// codeUint64 is uint 64.
	codeUint64 byte = 0xcf
	// codeInt8 is int 8.
	codeInt8 byte = 0xd0
	// codeInt16 is int 16.
	codeInt16 byte = 0xd1
	// codeInt32 is int 32.
	codeInt32 byte = 0xd2
	// codeInt64 is int 64.
	codeInt64 byte = 0xd3
	// codeFixext4 is fixext 4, the form of a 32-bit timestamp.
	codeFixext4 byte = 0xd6
	// codeFixext8 is fixext 8, the form of a 64-bit timestamp.
	codeFixext8 byte = 0xd7
	// codeStr8 is str 8.
	codeStr8 byte = 0xd9
	// codeStr16 is str 16.
	codeStr16 byte = 0xda
	// codeStr32 is str 32.
	codeStr32 byte = 0xdb
	// codeArray16 is array 16.
	codeArray16 byte = 0xdc
	// codeArray32 is array 32.
	codeArray32 byte = 0xdd
	// codeMap16 is map 16.
	codeMap16 byte = 0xde
	// codeMap32 is map 32.
	codeMap32 byte = 0xdf
)

// Widths, in bytes, of the field that follows a header byte: each family's
// fields double from one width to the next.
const (
	// width8 is a one-byte field.
	width8 uint8 = 1 << iota
	// width16 is a two-byte big-endian field.
	width16
	// width32 is a four-byte big-endian field.
	width32
	// width64 is an eight-byte big-endian field.
	width64
)

// fixext16Size is the payload size of fixext 16.
const fixext16Size uint64 = 16

// The timestamp extension type (spec.md §Timestamp extension type).
const (
	// extTimestamp is the extension type the specification reserves for the
	// timestamp.
	extTimestamp int8 = -1
	// extTimestampByte is extTimestamp as the byte on the wire.
	extTimestampByte byte = 0xff
)

// Bounds every encode and decode is held to.
const (
	// maxMsgPackBytes caps the Unmarshal input and the whole of one stream at
	// 10 MiB, which comfortably covers every realistic configuration or event
	// document while bounding what one hostile input can make the process
	// hold (CWE-400).
	maxMsgPackBytes int = 10 << 20
	// maxDepth bounds how deeply containers nest — arrays, maps and structs
	// on decode, and every pointer, interface and container on encode — so
	// neither recursion can exhaust a goroutine's stack (CWE-674) and a
	// cyclic value fails instead of recursing forever. Encode counts at least
	// as many levels as the decode of its output, so anything Marshal writes,
	// Unmarshal reads back.
	maxDepth int = 1000
	// preallocBytes is the most memory a DECLARED length may reserve before
	// the elements behind it have been read; past it, a slice or map grows as
	// elements actually arrive (CWE-1284).
	preallocBytes int = 64 << 10
	// frameChunk is the largest read the stream framer issues for one string,
	// binary or extension payload, so a declared length is paid for only as
	// its bytes arrive.
	frameChunk uint64 = 64 << 10
	// formatBlockLen is the number of format bytes in 0xc0–0xdf.
	formatBlockLen int = 32
	// headerTableLen is the number of possible header bytes.
	headerTableLen int = 256
)

// family is what a header byte announces.
type family uint8

// The families of the MessagePack type system, plus famInvalid for the byte
// the specification never uses.
const (
	// famInvalid is 0xc1, never used.
	famInvalid family = iota
	// famNil is nil.
	famNil
	// famBool is true or false.
	famBool
	// famInt is a signed integer: negative fixint, positive fixint or int 8–64.
	famInt
	// famUint is an unsigned integer: uint 8–64.
	famUint
	// famFloat32 is float 32.
	famFloat32
	// famFloat64 is float 64.
	famFloat64
	// famStr is a UTF-8 string.
	famStr
	// famBin is a byte array.
	famBin
	// famArray is an array.
	famArray
	// famMap is a map.
	famMap
	// famExt is an extension: a type byte and an opaque payload.
	famExt
)

// headerInfo describes one header byte.
type headerInfo struct {
	// inline is what the byte itself carries: a fixint's value (two's
	// complement), a fix form's count or length, or a fixext's payload size.
	inline uint64
	// fam is the family the byte announces.
	fam family
	// width is the size of the length or value field after the byte; zero
	// when the byte carries everything itself.
	width uint8
}

// header is one decoded header: the family, and the value or length it
// carries.
type header struct {
	// arg is a number's raw value (an int's two's complement, a float's
	// bits), or the length of a str, bin or ext payload, or the element count
	// of an array or map.
	arg uint64
	// fam is the family the header announced.
	fam family
	// width is the size of the field the value was read from — zero for a
	// fix form — which tells an untyped decode which Go width to produce.
	width uint8
	// ext is an extension's type; meaningful only when fam is famExt.
	ext int8
}

// Package-level lookup tables, built once at initialisation.
var (
	// formatBlock describes 0xc0–0xdf, indexed by byte − codeNil.
	formatBlock = [formatBlockLen]headerInfo{
		{fam: famNil},
		{fam: famInvalid},
		{fam: famBool},
		{fam: famBool, inline: 1},
		{fam: famBin, width: width8},
		{fam: famBin, width: width16},
		{fam: famBin, width: width32},
		{fam: famExt, width: width8},
		{fam: famExt, width: width16},
		{fam: famExt, width: width32},
		{fam: famFloat32, width: width32},
		{fam: famFloat64, width: width64},
		{fam: famUint, width: width8},
		{fam: famUint, width: width16},
		{fam: famUint, width: width32},
		{fam: famUint, width: width64},
		{fam: famInt, width: width8},
		{fam: famInt, width: width16},
		{fam: famInt, width: width32},
		{fam: famInt, width: width64},
		{fam: famExt, inline: uint64(width8)},
		{fam: famExt, inline: uint64(width16)},
		{fam: famExt, inline: uint64(width32)},
		{fam: famExt, inline: uint64(width64)},
		{fam: famExt, inline: fixext16Size},
		{fam: famStr, width: width8},
		{fam: famStr, width: width16},
		{fam: famStr, width: width32},
		{fam: famArray, width: width16},
		{fam: famArray, width: width32},
		{fam: famMap, width: width16},
		{fam: famMap, width: width32},
	}

	// headerTable describes every possible header byte.
	headerTable = buildHeaderTable()
)

// buildHeaderTable classifies each of the 256 header bytes once, so the hot
// path is one indexed load per value instead of a cascade of comparisons.
func buildHeaderTable() [headerTableLen]headerInfo {
	//: one entry per byte value.
	var table [headerTableLen]headerInfo
	//: classify every byte.
	for i := range headerTableLen {
		//: the index is the header byte.
		table[i] = classifyHeader(byte(i))
	}
	//: hand back the finished table.
	return table
}

// classifyHeader describes one header byte from the ranges of spec.md.
func classifyHeader(c byte) headerInfo {
	//: the ranges are disjoint and cover all 256 bytes.
	switch {
	//: 0x00–0x7f positive fixint: the byte is the value.
	case c <= posFixintMax:
		return headerInfo{fam: famInt, inline: uint64(c)}
	//: 0x80–0x8f fixmap: the low nibble is the pair count.
	case c < fixarrayPrefix:
		return headerInfo{fam: famMap, inline: uint64(c & fixCountMask)}
	//: 0x90–0x9f fixarray: the low nibble is the element count.
	case c < fixstrPrefix:
		return headerInfo{fam: famArray, inline: uint64(c & fixCountMask)}
	//: 0xa0–0xbf fixstr: the low five bits are the byte length.
	case c < codeNil:
		return headerInfo{fam: famStr, inline: uint64(c & fixstrLenMask)}
	//: 0xe0–0xff negative fixint: the byte is the value, sign-extended.
	case c >= negFixintMin:
		return headerInfo{fam: famInt, inline: uint64(int64(int8(c)))}
	//: 0xc0–0xdf: the format block, one entry per byte.
	default:
		return formatBlock[c-codeNil]
	}
}
