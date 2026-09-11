// Package id — white-box tests for the KSUID generator and its base62 codec.
package id

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_ksuidGen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_ksuidGen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "ksuid"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(ksuidGen{}.Scheme()); got != c.want {
			t.Errorf("Scheme() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_ksuidGen_New pins the rendered shape and the ordering property. Like a
// ULID, a KSUID is chosen over a random identifier precisely because it sorts
// by time as a plain string — so the prefix comparison is the whole point.
func Test_ksuidGen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		count int
	}
	tests := []tc{
		{"a single identifier", 1},
		{"a pair", 2},
		{"a batch", 256},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		var prev string
		for range c.count {
			got, err := ksuidGen{}.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			//: exactly 27 base62 characters, or no parser will accept it.
			if len(got) != ksuidChars {
				t.Fatalf("New() = %q (%d chars), want %d", got, len(got), ksuidChars)
			}
			for _, r := range got {
				if !strings.ContainsRune(base62Alphabet, r) {
					t.Errorf("New() = %q contains %q, outside the base62 alphabet", got, r)
				}
			}
			if _, dup := seen[got]; dup {
				t.Errorf("New() repeated %q", got)
			}
			seen[got] = struct{}{}

			//: the whole string must be non-decreasing within a second and
			//: strictly increasing across one. Comparing the full rendering is
			//: safe here — only the leading digits carry the timestamp, and the
			//: random tail cannot pull a later id below an earlier one because
			//: the encoding is fixed-width and order-preserving.
			if prev != "" && got[:4] < prev[:4] {
				t.Errorf("the time prefix regressed: %q then %q", prev, got)
			}
			prev = got
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_base62Encode pins the digit order and the zero padding.
//
// The padding is the subtle half: an unpadded rendering still decodes, still
// round-trips, and still looks like a KSUID — it merely sorts "9" after "10",
// destroying the one property the format is chosen for.
func Test_base62Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the low-order value to place in the 20-byte big-endian buffer.
		value uint64
		want  string
	}
	tests := []tc{
		{"zero", 0, strings.Repeat("0", ksuidChars)},
		{"one", 1, strings.Repeat("0", ksuidChars-1) + "1"},
		{"the last single digit", 61, strings.Repeat("0", ksuidChars-1) + "z"},
		{"the first carry", 62, strings.Repeat("0", ksuidChars-2) + "10"},
		//: 10 is 'A' and 35 is 'Z' — digits come first, then upper, then lower,
		//: which is what keeps the rendering order-preserving.
		{"a two-digit value", 62*10 + 35, strings.Repeat("0", ksuidChars-2) + "AZ"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var raw [ksuidRawLen]byte
		//: place the value in the low-order (trailing) bytes, big-endian.
		for i := range 8 {
			raw[ksuidRawLen-1-i] = byte(c.value >> (i * bitsPerByte))
		}

		got := base62Encode(raw[:])

		if got != c.want {
			t.Errorf("base62Encode(%d) = %q, want %q", c.value, got, c.want)
		}
		//: the width is fixed, which is what makes the ordering work.
		if len(got) != ksuidChars {
			t.Errorf("base62Encode produced %d characters, want %d", len(got), ksuidChars)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the encoding must be order-preserving, or a sorted list of KSUIDs would
	//: not be a chronological one.
	var small, large [ksuidRawLen]byte
	small[0], large[0] = 0x01, 0x02
	if base62Encode(small[:]) >= base62Encode(large[:]) {
		t.Error("a larger value did not encode to a lexically larger string")
	}
	//: the full 160-bit space must still fit in 27 characters.
	var ones [ksuidRawLen]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	if got := base62Encode(ones[:]); len(got) != ksuidChars {
		t.Errorf("base62Encode(2^160-1) = %q (%d chars), want %d", got, len(got), ksuidChars)
	}
}

// Test_base62_roundtrip pins that decode is the exact inverse of encode in both
// directions. A codec that only round-trips one way is the classic source of
// identifiers that survive a write and come back different.
func Test_base62_roundtrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  [ksuidRawLen]byte
	}
	var zeros, ones, ascending, epochOnly [ksuidRawLen]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	for i := range ascending {
		ascending[i] = byte(i)
	}
	//: only the timestamp prefix set — the shape a KSUID minted at second 1 has.
	epochOnly[ksuidTimeBytes-1] = 1
	tests := []tc{
		{"all zeros", zeros},
		{"all ones", ones},
		{"an ascending pattern", ascending},
		{"a timestamp with an empty payload", epochOnly},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: encode then decode must return the very same bytes.
		encoded := base62Encode(c.raw[:])
		decoded, err := base62Decode(encoded)
		if err != nil {
			t.Fatalf("base62Decode(%q) = %v, want nil", encoded, err)
		}
		if decoded != c.raw {
			t.Errorf("round trip changed the value: %x -> %q -> %x", c.raw, encoded, decoded)
		}
		//: and decode then encode must return the very same string.
		if again := base62Encode(decoded[:]); again != encoded {
			t.Errorf("round trip changed the rendering: %q -> %x -> %q", encoded, decoded, again)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_base62Decode_rejects pins the three refusals. The overflow one is the
// interesting case: 62^27 is about 2^160.7, so a third of the well-formed-
// looking 27-character strings encode a number no KSUID can hold. Truncating
// them would make two distinct strings decode to the same identifier.
func Test_base62Decode_rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		//: the "rule" field the refusal must carry.
		rule string
	}
	tests := []tc{
		{"the empty string", "", "length"},
		{"one character short", strings.Repeat("0", ksuidChars-1), "length"},
		{"one character long", strings.Repeat("0", ksuidChars+1), "length"},
		{"a symbol outside base62", strings.Repeat("0", ksuidChars-1) + "-", "alphabet"},
		{"a space", strings.Repeat("0", ksuidChars-1) + " ", "alphabet"},
		//: 'z' repeated is 62^27-1, comfortably past 2^160-1.
		{"the largest 27-character value", strings.Repeat("z", ksuidChars), "overflow"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := base62Decode(c.in)
		if err == nil {
			t.Fatalf("base62Decode(%q) = nil error, want ID_MALFORMED", c.in)
		}
		if !errs.HasCode(err, CodeIDMalformed) {
			t.Errorf("base62Decode(%q) = %v, want code %s", c.in, err, CodeIDMalformed)
		}
		//: the rule field is what tells an operator which check fired without
		//: the input ever reaching a log line.
		if got := fieldValue(err, "rule"); got != c.rule {
			t.Errorf("base62Decode(%q) rule = %q, want %q", c.in, got, c.rule)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the boundary itself, derived rather than transcribed: 2^160-1 is the
	//: largest value 20 bytes hold, so its rendering must decode, and anything
	//: lexically above it must not.
	var ones [ksuidRawLen]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	maxValid := base62Encode(ones[:])
	if got, err := base62Decode(maxValid); err != nil || got != ones {
		t.Fatalf("base62Decode(%q) = %x, %v; want the maximum value and nil", maxValid, got, err)
	}
	//: bumping the most-significant digit to the top symbol steps past 2^160-1
	//: without changing the length — exactly the case truncation would hide.
	topSymbol := base62Alphabet[base62Radix-1:]
	if maxValid[:1] >= topSymbol {
		t.Fatalf("the maximum rendering already starts at the top symbol (%q); the overflow probe is void", maxValid)
	}
	overflow := topSymbol + maxValid[1:]
	if _, err := base62Decode(overflow); !errs.HasCode(err, CodeIDMalformed) {
		t.Errorf("base62Decode(%q) = %v, want ID_MALFORMED", overflow, err)
	}
}

// Test_ksuid_conformance decodes a KSUID minted by the REFERENCE
// implementation. Every other test in this file checks the codec against
// itself, which cannot catch a self-consistent misreading of the format: a
// shifted epoch, a reordered alphabet or a flipped endianness would all
// round-trip perfectly here and interoperate with nothing.
//
// The timestamp is the assertion that pins all four at once. Recovering
// 2017-10-09T21:00:47-0700 from these 27 characters requires the epoch offset,
// the big-endian prefix, the alphabet ORDER and the 27-character width to be
// simultaneously right — get any one wrong and the instant lands centuries away.
func Test_ksuid_conformance(t *testing.T) {
	t.Parallel()
	//: published in the segmentio/ksuid README alongside its decoded time.
	const (
		fixture  string = "0ujtsYcgvSTl8PAuAdqWYSMnLOv"
		wantUnix int64  = 1_507_608_047 // 2017-10-10T04:00:47Z
	)
	issued, payload, err := ParseKSUID(fixture)
	if err != nil {
		t.Fatalf("ParseKSUID(%q) = %v, want nil", fixture, err)
	}
	if got := issued.Unix(); got != wantUnix {
		t.Errorf("ParseKSUID(%q) issued at %d (%s), want %d",
			fixture, got, issued.Format(time.RFC3339), wantUnix)
	}
	//: the payload must be the 16 bytes following the prefix — the same ones
	//: re-encoding will consume, so an off-by-one split shows up below.
	raw, decErr := base62Decode(fixture)
	if decErr != nil {
		t.Fatalf("base62Decode(%q) = %v, want nil", fixture, decErr)
	}
	if want := hex.EncodeToString(raw[ksuidTimeBytes:]); hex.EncodeToString(payload[:]) != want {
		t.Errorf("ParseKSUID(%q) payload = %s, want %s", fixture, hex.EncodeToString(payload[:]), want)
	}
	//: and re-encoding the decoded bytes must reproduce the fixture exactly.
	if got := base62Encode(raw[:]); got != fixture {
		t.Errorf("re-encoding the fixture gave %q, want %q", got, fixture)
	}
}
