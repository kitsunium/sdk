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

// Compile-time guard — int must be 8 bytes so Code()/Layer() int accessors
// never lose information via int(uint32) casting. The build tag above
// enforces 64-bit GOARCH; this static assertion is belt-and-braces for
// exotic build configurations that might bypass the tag.
var _ = [1]struct{}{}[8-unsafe.Sizeof(int(0))]

// Pack constructs a Code from its four octets. RUNTIME only — sentinel
// constants MUST use hex literals so they stay const-expressible.
//
// Params:
//   - m: Major octet (SemVer major: 0=internal, 1=v1, 2=v2, ...)
//   - l: Layer octet within the major.
//   - p: Package octet within the layer.
//   - s: Serial octet within the package.
//
// Returns:
//   - Code: the packed 32-bit identifier.
func Pack(m Major, l Layer, p PkgCode, s Serial) (c Code) {
	//: shift each octet into place — bitwise OR is branch-free.
	return Code(m)<<shiftMajor | Code(l)<<shiftLayer | Code(p)<<shiftPackage | Code(s)
}

// Major returns the top octet (SemVer major byte).
//
// Returns:
//   - Major: bits 24..31 of the Code.
func (c Code) Major() (m Major) {
	//: unsigned shift is safe and drops the lower 24 bits.
	return Major(c >> shiftMajor)
}

// Layer returns the second-highest octet.
//
// Returns:
//   - Layer: bits 16..23 of the Code.
func (c Code) Layer() (l Layer) {
	//: cast to uint8 truncates after the shift.
	return Layer(c >> shiftLayer)
}

// Package returns the third octet.
//
// Returns:
//   - PkgCode: bits 8..15 of the Code.
func (c Code) Package() (p PkgCode) {
	//: same shift-and-truncate pattern as the other accessors.
	return PkgCode(c >> shiftPackage)
}

// Serial returns the bottom octet.
//
// Returns:
//   - Serial: bits 0..7 of the Code.
func (c Code) Serial() (s Serial) {
	//: casting a uint32 to uint8 keeps only the low byte.
	return Serial(c)
}

// String returns the CANONICAL dotted form "M.L.P.S" (unpadded).
// This form is used for storage, logs, fixtures, and lookup keys.
//
// Returns:
//   - string: canonical dotted-quad representation.
func (c Code) String() (s string) {
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
func (c Code) Padded() (s string) {
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
//   - n: the byte to stringify.
//
// Returns:
//   - string: decimal form, 1 to 3 characters.
func itoaDecimal(n uint8) (s string) {
	//: strconv.Itoa is the single stdlib call; no fmt required.
	return strconv.Itoa(int(n))
}

// itoaPadded3 returns the zero-padded 3-digit decimal form ("000" .. "255").
//
// Params:
//   - n: the byte to stringify.
//
// Returns:
//   - string: always exactly 3 characters long.
func itoaPadded3(n uint8) (s string) {
	//: single switch keeps each branch at constant work; no fmt verbs.
	switch {
	case n < 10:
		//: single-digit input — prepend two zeros.
		return "00" + strconv.Itoa(int(n))
	case n < 100:
		//: double-digit input — prepend one zero.
		return "0" + strconv.Itoa(int(n))
	default:
		//: triple-digit input — already the right width.
		return strconv.Itoa(int(n))
	}
}
