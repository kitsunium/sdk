package errs

import (
	"strconv"
)

// Shift values are WIRE-STABLE. Changing them breaks every persisted Code.
// A future layout (e.g. 4/8/8/12) MUST introduce a new Code type, never
// mutate these constants.
const (
	shiftMajor   uint = 24
	shiftLayer   uint = 16
	shiftPackage uint = 8
)

// CIDR-style mask constants for prefix matching via NewPrefixMatcher.
const (
	MaskByMajor   Code = 0xFF_00_00_00 // /8 — all codes sharing Major
	MaskByLayer   Code = 0xFF_FF_00_00 // /16 — all codes sharing Major+Layer
	MaskByPackage Code = 0xFF_FF_FF_00 // /24 — all codes sharing Major+Layer+Package
	MaskExact     Code = 0xFF_FF_FF_FF // /32 — exact match only
)

// padThreshold10 is the cutoff below which itoaPadded3 must emit two
// leading zeros ("00X"). Named to avoid a bare magic literal.
const padThreshold10 uint8 = 10

// padThreshold100 is the cutoff below which itoaPadded3 must emit one
// leading zero ("0XX"). Named to avoid a bare magic literal.
const padThreshold100 uint8 = 100

// Code packs a 4-byte dotted identifier (MM.LL.PP.SS) as uint32.
// See ADR 0005 for the registry.
type Code uint32

// Named octet types prevent positional-argument footguns in Pack.
// PkgCode is spelled out (not `Pkg`) so callers keep `pkg` as an ordinary
// variable name without shadowing.
type (
	Major   uint8
	Layer   uint8
	PkgCode uint8
	Serial  uint8
)

// Pack constructs a Code from its four octets. RUNTIME only — sentinel
// constants MUST use hex literals so they stay const-expressible.
func Pack(mm Major, ll Layer, pp PkgCode, ss Serial) Code {
	//: shift each octet into place — bitwise OR is branch-free.
	return Code(mm)<<shiftMajor | Code(ll)<<shiftLayer | Code(pp)<<shiftPackage | Code(ss)
}

// Major returns the top octet (SemVer major byte).
func (c Code) Major() Major {
	//: unsigned shift is safe and drops the lower 24 bits.
	return Major(c >> shiftMajor)
}

// Layer returns the second-highest octet.
func (c Code) Layer() Layer {
	//: cast to uint8 truncates after the shift.
	return Layer(c >> shiftLayer)
}

// Package returns the third octet.
func (c Code) Package() PkgCode {
	//: same shift-and-truncate pattern as the other accessors.
	return PkgCode(c >> shiftPackage)
}

// Serial returns the bottom octet.
func (c Code) Serial() Serial {
	//: casting a uint32 to uint8 keeps only the low byte.
	return Serial(c)
}

// String returns the CANONICAL dotted form "M.L.P.S" (unpadded).
// This form is used for storage, logs, fixtures, and lookup keys.
func (c Code) String() string {
	//: rendered into a stack buffer and copied once: the result string is
	//: the only allocation, where a seven-operand concat paid a runtime
	//: concatstrings call on top of it.
	var buf [codeMaxLen]byte
	//: the one heap copy — the caller's string.
	return string(appendCode(buf[:0], c))
}

// Padded returns the zero-padded "MMM.LLL.PPP.SSS" form. DISPLAY ONLY —
// must never be used as a lookup key; the canonical form is String().
func (c Code) Padded() string {
	//: fixed width, so the stack buffer is filled exactly once and copied
	//: once — five allocations (four padded octets and the concat) become one.
	var buf [codeMaxLen]byte
	dst := appendPadded3(buf[:0], uint8(c.Major()))
	dst = append(dst, '.')
	dst = appendPadded3(dst, uint8(c.Layer()))
	dst = append(dst, '.')
	dst = appendPadded3(dst, uint8(c.Package()))
	dst = append(dst, '.')
	//: matches the width any dashboard or aligned-table consumer wants.
	return string(appendPadded3(dst, uint8(c.Serial())))
}

// codeMaxLen is the longest rendering of a Code in either form:
// "255.255.255.255" and "MMM.LLL.PPP.SSS" are both fifteen bytes.
const codeMaxLen int = 15

// appendCode appends the canonical dotted form of c — the bytes String
// returns — onto dst, so a renderer that composes a larger string (Error,
// PrefixMatcher.String) writes the code in place instead of allocating it.
func appendCode(dst []byte, c Code) []byte {
	dst = strconv.AppendUint(dst, uint64(c.Major()), decimalBase)
	dst = append(dst, '.')
	dst = strconv.AppendUint(dst, uint64(c.Layer()), decimalBase)
	dst = append(dst, '.')
	dst = strconv.AppendUint(dst, uint64(c.Package()), decimalBase)
	dst = append(dst, '.')
	//: hand back the buffer carrying the four octets.
	return strconv.AppendUint(dst, uint64(c.Serial()), decimalBase)
}

// codeLen returns len(c.String()) without rendering it, so a renderer can
// size its one allocation exactly.
func codeLen(c Code) int {
	//: three dots plus the width of each octet.
	return octetLen(uint8(c.Major())) + octetLen(uint8(c.Layer())) +
		octetLen(uint8(c.Package())) + octetLen(uint8(c.Serial())) + 3
}

// octetLen returns the number of decimal digits of an unpadded octet.
func octetLen(octet uint8) int {
	//: one comparison per width; an octet has at most three digits.
	switch {
	//: 0-9.
	case octet < padThreshold10:
		//: one digit.
		return 1
	//: 10-99.
	case octet < padThreshold100:
		//: two digits.
		return 2
	//: 100-255.
	default:
		//: three digits.
		return 3
	}
}

// itoaDecimal returns the decimal representation of a uint8 (0-255) UNPADDED.
// Internal helper for ParseCode's diagnostics; the renderers use appendCode.
func itoaDecimal(octet uint8) string {
	//: strconv.Itoa is the single stdlib call; no fmt required.
	return strconv.Itoa(int(octet))
}

// appendPadded3 appends the zero-padded 3-digit decimal form ("000".. "255")
// of octet onto dst.
func appendPadded3(dst []byte, octet uint8) []byte {
	//: hundreds, tens, units — each a digit, leading zeros included, which is
	//: exactly the padding the form asks for.
	return append(dst,
		'0'+octet/padThreshold100,
		'0'+octet/padThreshold10%padThreshold10,
		'0'+octet%padThreshold10)
}
