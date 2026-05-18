//go:build amd64 || arm64 || riscv64 || ppc64 || ppc64le || s390x

// Package errs: code.go defines the dotted-quad Code type introduced by
// ADR 0005. Layout: MM.LL.PP.SS over uint32 — Major.Layer.Package.Serial.
// Codes are comparable, ordered, map-keyable, and const-expressible via
// hex literals (Pack is a runtime constructor only).
package errs

import (
	"strconv"
	"unsafe"
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

// requiredIntSize is the expected size of `int` in bytes on supported
// 64-bit GOARCH targets. Used by the compile-time guard below so the
// magic number "8" stays named at its single point of use.
const requiredIntSize uintptr = 8

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

// Compile-time guard — int must be `requiredIntSize` bytes so Code()/Layer()
// int accessors never lose information via int(uint32) casting. The build
// tag above enforces 64-bit GOARCH; this static assertion is belt-and-braces
// for exotic build configurations that might bypass the tag.
var _ [requiredIntSize - unsafe.Sizeof(int(0))]struct{}

// Pack constructs a Code from its four octets. RUNTIME only — sentinel
// constants MUST use hex literals so they stay const-expressible.
//
// Params:
//   - mm: Major octet (SemVer major: 0=internal, 1=v1, 2=v2, ...)
//   - ll: Layer octet within the major.
//   - pp: Package octet within the layer.
//   - ss: Serial octet within the package.
//
// Returns:
//   - Code: the packed 32-bit identifier.
func Pack(mm Major, ll Layer, pp PkgCode, ss Serial) Code {
	//: shift each octet into place — bitwise OR is branch-free.
	return Code(mm)<<shiftMajor | Code(ll)<<shiftLayer | Code(pp)<<shiftPackage | Code(ss)
}

// Major returns the top octet (SemVer major byte).
//
// Returns:
//   - Major: bits 24..31 of the Code.
func (c Code) Major() Major {
	//: unsigned shift is safe and drops the lower 24 bits.
	return Major(c >> shiftMajor)
}

// Layer returns the second-highest octet.
//
// Returns:
//   - Layer: bits 16..23 of the Code.
func (c Code) Layer() Layer {
	//: cast to uint8 truncates after the shift.
	return Layer(c >> shiftLayer)
}

// Package returns the third octet.
//
// Returns:
//   - PkgCode: bits 8..15 of the Code.
func (c Code) Package() PkgCode {
	//: same shift-and-truncate pattern as the other accessors.
	return PkgCode(c >> shiftPackage)
}

// Serial returns the bottom octet.
//
// Returns:
//   - Serial: bits 0..7 of the Code.
func (c Code) Serial() Serial {
	//: casting a uint32 to uint8 keeps only the low byte.
	return Serial(c)
}

// String returns the CANONICAL dotted form "M.L.P.S" (unpadded).
// This form is used for storage, logs, fixtures, and lookup keys.
//
// Returns:
//   - string: canonical dotted-quad representation.
func (c Code) String() string {
	//: manual concat avoids the fmt import and keeps the kernel package's
	//: zero-alloc discipline (strconv.Itoa is the only helper we need).
	return itoaDecimal(uint8(c.Major())) + "." +
		itoaDecimal(uint8(c.Layer())) + "." +
		itoaDecimal(uint8(c.Package())) + "." +
		itoaDecimal(uint8(c.Serial()))
}

// Padded returns the zero-padded "MMM.LLL.PPP.SSS" form. DISPLAY ONLY —
// must never be used as a lookup key; the canonical form is String().
//
// Returns:
//   - string: zero-padded dotted-quad representation.
func (c Code) Padded() string {
	//: matches the width any dashboard or aligned-table consumer wants.
	return itoaPadded3(uint8(c.Major())) + "." +
		itoaPadded3(uint8(c.Layer())) + "." +
		itoaPadded3(uint8(c.Package())) + "." +
		itoaPadded3(uint8(c.Serial()))
}

// itoaDecimal returns the decimal representation of a uint8 (0-255) UNPADDED.
// Internal helper — exported sibling is Code.String().
//
// Params:
//   - octet: the byte to stringify.
//
// Returns:
//   - string: decimal form, 1 to 3 characters.
func itoaDecimal(octet uint8) string {
	//: strconv.Itoa is the single stdlib call; no fmt required.
	return strconv.Itoa(int(octet))
}

// itoaPadded3 returns the zero-padded 3-digit decimal form ("000" .. "255").
//
// Params:
//   - octet: the byte to stringify.
//
// Returns:
//   - string: always exactly 3 characters long.
func itoaPadded3(octet uint8) string {
	//: single switch keeps each branch at constant work; no fmt verbs.
	switch {
	//: single-digit input — prepend two zeros to reach 3-char width.
	case octet < padThreshold10:
		//: emit the "00X" form using a leading two-zero prefix.
		return "00" + strconv.Itoa(int(octet))
	//: double-digit input — prepend one zero to reach 3-char width.
	case octet < padThreshold100:
		//: emit the "0XX" form using a single leading zero.
		return "0" + strconv.Itoa(int(octet))
	//: triple-digit input — already the right width, emit as-is.
	default:
		//: forward the 3-digit decimal directly with no padding.
		return strconv.Itoa(int(octet))
	}
}
