// Package errs_test — fuzz coverage for ParseCode, the strict canonical parser.
// The target drives both directions of the canonical form from one corpus entry:
// arbitrary text through ParseCode, and four fuzzed octets through
// Pack → String → ParseCode. internal/kernel is stdlib-only, so this file
// imports nothing beyond the stdlib and the package under test.
package errs_test

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Canonical-form envelope restated from parse.go. They are unexported there, so
// the black-box target pins them: a canonical form Code.String() can emit that
// falls outside this envelope would make ParseCode reject its own output.
const (
	// fuzzCodeStringMinLen is the shortest canonical form, "0.0.0.0".
	fuzzCodeStringMinLen int = 7
	// fuzzCodeStringMaxLen is the longest canonical form, "255.255.255.255".
	fuzzCodeStringMaxLen int = 15
)

// fuzzParseFailReason is the single reason every ParseCode rejection carries.
const fuzzParseFailReason string = "INVALID_CODE_STRING"

// Bit positions of the four dotted-quad fields inside the packed word the fuzz
// target carries, Major highest.
const (
	// fuzzOctetShift1 is the shift of the third field (PkgCode).
	fuzzOctetShift1 uint32 = 8
	// fuzzOctetShift2 is the shift of the second field (Layer).
	fuzzOctetShift2 uint32 = 16
	// fuzzOctetShift3 is the shift of the first field (Major).
	fuzzOctetShift3 uint32 = 24
)

// FuzzParseCode drives both directions of the canonical-form contract.
//
// Invariants asserted (beyond "it does not panic", which the runtime enforces):
//
//  1. CANONICAL FORM IS UNIQUE — if ParseCode(s) succeeds with Code c, then
//     c.String() must equal s BYTE FOR BYTE. This is the property parse.go's
//     doc-comment declares ("the canonical form is the only textual key that
//     maps to a Code", ADR 0005 §3.2): at most one string may map to any Code.
//  2. PADDED IS NOT AN INPUT — for a parsed c whose Padded() differs from its
//     String(), ParseCode(c.Padded()) must FAIL. Same declaration, stated as the
//     exclusion it actually is.
//  3. ZERO SENTINEL — a success never yields Code(0), and a failure always
//     yields Code(0) alongside a typed *errs.Error carrying
//     CodeInvalidCodeString / INVALID_CODE_STRING.
//  4. INVERSE — for four fuzzed octets, Pack(...).String() must land inside the
//     [7,15] envelope and must reparse to exactly the Code it came from (except
//     Code(0), whose canonical text is the reserved sentinel ParseCode refuses).
func FuzzParseCode(f *testing.F) {
	//: seed both directions from one corpus shape.
	fuzzSeedParseCode(f)
	//: s drives the text direction; the packed word drives the inverse. The
	//: four octets travel as ONE uint32 rather than as four separate byte
	//: parameters because that is exactly what a Code is — a packed word — so
	//: the mutator's bit flips land on the value under test instead of on four
	//: independent arguments it has to keep consistent.
	f.Fuzz(func(t *testing.T, s string, packed uint32) {
		//: direction 1 — arbitrary text must parse to exactly one canonical form.
		fuzzAssertParseCanonical(t, s)
		//: direction 2 — every packed Code must survive String → ParseCode.
		fuzzAssertPackInverse(t, packed)
	})
}

// fuzzSeedParseCode adds the canonical accepts, every documented reject shape,
// and octet tuples covering the boundaries of the four-byte space.
func fuzzSeedParseCode(f *testing.F) {
	//: accepted canonical forms — the only inputs ParseCode may take.
	accepts := []string{
		"0.0.0.1",
		"255.255.255.255",
		"1.2.3.4",
	}
	//: every rejection shape parse.go's doc-comment enumerates, plus the
	//: byte-level degenerates a text parser has to survive.
	rejects := []string{
		"",
		"0.0.0.0",
		"001.1.1.1",
		"1.1.1",
		"1.1.1.1.1",
		"256.1.1.1",
		"1.1.1.1 ",
		" 1.1.1.1",
		"+1.1.1.1",
		"1..1.1",
		"1.1.1.-1",
		"1234567890123456",
		"é.1.1.1",
		"1.1.1.ÿ",
		"1.1.1.１",
		"1.1.1.\xff\xfe\xfd",
	}
	//: the text leg gets every shape, paired with a neutral octet tuple.
	for _, s := range append(append([]string{}, accepts...), rejects...) {
		//: one corpus entry per text shape, paired with a neutral Code.
		f.Add(s, fuzzPackOctets([4]byte{0, 0, 0, 1}))
	}
	//: octet tuples: the zero sentinel, both bounds, the padding thresholds.
	octets := [][4]byte{
		{0, 0, 0, 0},
		{0, 0, 0, 1},
		{255, 255, 255, 255},
		{1, 2, 3, 4},
		{0, 1, 0, 0},
		{9, 10, 99, 100},
		{100, 200, 50, 5},
	}
	//: the inverse leg gets every tuple, paired with a canonical text.
	for _, o := range octets {
		//: one corpus entry per octet tuple.
		f.Add("1.2.3.4", fuzzPackOctets(o))
	}
}

// fuzzPackOctets folds a four-octet tuple into the single uint32 the fuzz
// target carries, most-significant octet first — the same order Code.String()
// renders them in.
func fuzzPackOctets(o [4]byte) uint32 {
	//: Major occupies the high octet, Serial the low one.
	return uint32(o[0])<<fuzzOctetShift3 | uint32(o[1])<<fuzzOctetShift2 |
		uint32(o[2])<<fuzzOctetShift1 | uint32(o[3])
}

// fuzzAssertParseCanonical drives the text direction: ParseCode over arbitrary
// bytes, with the success and failure contracts both pinned.
func fuzzAssertParseCanonical(t *testing.T, s string) {
	//: the verdict under test.
	c, err := errs.ParseCode(s)
	//: a rejection has its own contract — code, type, reason.
	if err != nil {
		//: check the failure shape and stop.
		fuzzAssertRejection(t, s, c, err)
		//: nothing round-trips from a rejected input.
		return
	}
	//: the reserved sentinel must never be produced by a successful parse.
	if c == 0 {
		//: Define/Wrap refuse Code(0), so parsing it would yield an unusable value.
		t.Fatalf("ParseCode(%q): succeeded with the reserved zero Code", s)
	}
	//: THE invariant — one Code, one text. Anything else means two strings map
	//: to the same Code, which is exactly what parse.go refuses to allow.
	if got := c.String(); got != s {
		//: a mismatch is a second textual representation slipping through.
		t.Fatalf("ParseCode(%q) = %#08x, whose String() is %q — canonical form is not unique", s, uint32(c), got)
	}
	//: and the Padded() form must stay outside the accepted language.
	fuzzAssertPaddedRejected(t, c)
}

// fuzzAssertRejection pins the failure contract: zero Code, typed error, one
// reason, one code — no variation the caller would have to switch on.
func fuzzAssertRejection(t *testing.T, s string, c errs.Code, err error) {
	//: a failure must not leak a partially-parsed value.
	if c != 0 {
		//: a non-zero Code beside an error is a value that escaped validation.
		t.Fatalf("ParseCode(%q): failed with %v but returned Code %#08x", s, err, uint32(c))
	}
	//: the typed-errors-only rule applies to the parser too.
	var typed *errs.Error
	//: an untyped error would break every caller's sentinel match.
	if !errors.As(err, &typed) {
		//: name the offending type.
		t.Fatalf("ParseCode(%q): want *errs.Error, got %T (%v)", s, err, err)
	}
	//: the code is frozen — callers match on it.
	if typed.Code() != errs.CodeInvalidCodeString {
		//: code drift breaks sentinel matching.
		t.Fatalf("ParseCode(%q): want Code %#08x, got %#08x", s, uint32(errs.CodeInvalidCodeString), uint32(typed.Code()))
	}
	//: the reason is frozen — log and telemetry filters match on it.
	if typed.Reason() != fuzzParseFailReason {
		//: reason drift breaks observability filters.
		t.Fatalf("ParseCode(%q): want Reason %q, got %q", s, fuzzParseFailReason, typed.Reason())
	}
}

// fuzzAssertPaddedRejected checks that the display-only Padded() form is not a
// second key for the same Code.
func fuzzAssertPaddedRejected(t *testing.T, c errs.Code) {
	//: when every octet is three digits Padded() IS the canonical form.
	padded := c.Padded()
	//: only the forms that actually differ are excluded inputs.
	if padded == c.String() {
		//: nothing to exclude.
		return
	}
	//: the padded form must not parse — it would be a second textual key.
	if _, err := errs.ParseCode(padded); err == nil {
		//: two strings mapping to one Code is the violation ADR 0005 §3.2 names.
		t.Fatalf("ParseCode(%q): the Padded() form of %s must be rejected", padded, c.String())
	}
}

// fuzzAssertPackInverse drives the inverse direction: every packed Code must
// render to a canonical form its own parser accepts and reads back identically.
func fuzzAssertPackInverse(t *testing.T, packed uint32) {
	//: unfold the word back into the four fields Pack names. Going through
	//: Pack rather than through a Code conversion keeps the assertion honest
	//: about the layout: if Pack ever ordered its fields differently, this
	//: still builds the Code the way production code does.
	want := errs.Pack(
		errs.Major(packed>>fuzzOctetShift3),
		errs.Layer(packed>>fuzzOctetShift2),
		errs.PkgCode(packed>>fuzzOctetShift1),
		errs.Serial(packed),
	)
	//: the canonical rendering of that Code.
	text := want.String()
	//: Code(0) renders to the reserved sentinel, which the parser must refuse.
	if want == 0 {
		//: the refusal is the contract here, not a round trip.
		if _, err := errs.ParseCode(text); err == nil {
			//: accepting it would hand callers a Code no sentinel can match.
			t.Fatalf("ParseCode(%q): the reserved zero sentinel must be rejected", text)
		}
		//: nothing further for the zero Code.
		return
	}
	//: the canonical form must fit the envelope ParseCode enforces up front —
	//: a String() outside it would make the parser reject its own output.
	if len(text) < fuzzCodeStringMinLen || len(text) > fuzzCodeStringMaxLen {
		//: the two sides of the contract have drifted apart.
		t.Fatalf("Code(%#08x).String() = %q: %d bytes, outside [%d,%d]", uint32(want), text, len(text), fuzzCodeStringMinLen, fuzzCodeStringMaxLen)
	}
	//: reparse the canonical form.
	got, err := errs.ParseCode(text)
	//: a Code the parser cannot read back is a hole in the round trip.
	if err != nil {
		//: surface which Code failed.
		t.Fatalf("ParseCode(%q) from Code %#08x: unexpected error %v", text, uint32(want), err)
	}
	//: and it must read back to the very same value.
	if got != want {
		//: a mismatch means Pack/String/ParseCode disagree on the layout.
		t.Fatalf("ParseCode(%q) = %#08x, want %#08x", text, uint32(got), uint32(want))
	}
}
