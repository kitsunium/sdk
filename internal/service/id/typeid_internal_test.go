// Package id — white-box tests for the TypeID generator and its prefix rules.
package id

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_typeIDGen_Scheme pins the scheme key. Unlike its siblings this key is
// NOT registered — see Test_typeIDGen_notRegistered — but it is still the name
// the generator reports, so it belongs to the same frozen vocabulary.
func Test_typeIDGen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "typeid"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(typeIDGen{}.Scheme()); got != c.want {
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

// Test_typeIDGen_New pins the rendered shape and the ordering property. A
// TypeID's payload is a UUIDv7, so successive identifiers under the SAME prefix
// must sort chronologically — that is what makes it a usable primary key.
func Test_typeIDGen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		count  int
	}
	tests := []tc{
		{"a single identifier", "user", 1},
		{"a batch", "user", 256},
		{"a prefix containing underscores", "user_account", 16},
		{"the shortest legal prefix", "u", 16},
		{"the longest legal prefix", strings.Repeat("a", typeIDMaxPrefix), 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gen := typeIDGen{prefix: c.prefix}
		seen := make(map[string]struct{}, c.count)
		var prev string
		for range c.count {
			got, err := gen.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			//: prefix, separator, then exactly 26 base32 characters.
			want := len(c.prefix) + len(typeIDSeparator) + crockfordChars
			if len(got) != want {
				t.Fatalf("New() = %q (%d chars), want %d", got, len(got), want)
			}
			if !strings.HasPrefix(got, c.prefix+typeIDSeparator) {
				t.Fatalf("New() = %q does not carry the %q prefix", got, c.prefix)
			}
			suffix := got[len(c.prefix)+len(typeIDSeparator):]
			for _, r := range suffix {
				if !strings.ContainsRune(typeIDAlphabet, r) {
					t.Errorf("New() = %q has %q in its suffix, outside the alphabet", got, r)
				}
			}
			//: the suffix must be lower case — the whole point of not reusing
			//: ULID's rendering is that TypeID compares case-sensitively.
			if suffix != strings.ToLower(suffix) {
				t.Errorf("New() = %q has an uppercase suffix", got)
			}
			if _, dup := seen[got]; dup {
				t.Errorf("New() repeated %q", got)
			}
			seen[got] = struct{}{}
			//: the 10-character time prefix of the suffix must never regress.
			if prev != "" && suffix[:10] < prev[:10] {
				t.Errorf("the time prefix regressed: %q then %q", prev, suffix)
			}
			prev = suffix
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_typeIDGen_zeroValue pins that the zero value refuses instead of minting
// "_01h2…". There is no default prefix to fall back on — that absence is
// exactly why the scheme is not registered — so ADR 0031 leaves only the
// explicit refusal.
func Test_typeIDGen_zeroValue(t *testing.T) {
	t.Parallel()
	got, err := typeIDGen{}.New()
	if err == nil {
		t.Fatalf("New() = %q, nil; want an ID_INVALID_PREFIX refusal", got)
	}
	if got != "" {
		t.Errorf("New() = %q alongside its refusal, want an empty string", got)
	}
	if !errs.HasCode(err, CodeIDInvalidPrefix) {
		t.Errorf("New() = %v, want code %s", err, CodeIDInvalidPrefix)
	}
}

// Test_validateTypePrefix is the strictness gate. Every accepted variation is a
// second spelling for the same entity — "User", "user_" and "user" naming one
// type in three ways is the confusion a typed identifier exists to remove, so
// the rules are enforced rather than normalised.
func Test_validateTypePrefix(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		//: "" means the prefix must be accepted; otherwise the expected rule.
		rule string
	}
	tests := []tc{
		{"a plain lowercase prefix", "user", ""},
		{"a single character", "u", ""},
		{"an interior underscore", "user_account", ""},
		{"several interior underscores", "a_b_c_d", ""},
		{"the maximum length", strings.Repeat("a", typeIDMaxPrefix), ""},
		{"the empty prefix", "", "empty"},
		{"one character too long", strings.Repeat("a", typeIDMaxPrefix+1), "too_long"},
		{"a leading underscore", "_user", "charset"},
		{"a trailing underscore", "user_", "charset"},
		{"a lone underscore", "_", "charset"},
		{"two underscores at the end", "user__", "charset"},
		{"an uppercase letter", "User", "charset"},
		{"an all-uppercase prefix", "USER", "charset"},
		{"a digit", "user1", "charset"},
		{"a hyphen", "user-account", "charset"},
		{"a dot", "user.account", "charset"},
		{"a space", "user account", "charset"},
		{"a non-ASCII letter", "usér", "charset"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := validateTypePrefix(c.prefix)
		if c.rule == "" {
			if err != nil {
				t.Errorf("validateTypePrefix(%q) = %v, want nil", c.prefix, err)
			}
			return
		}
		if err == nil {
			t.Fatalf("validateTypePrefix(%q) = nil, want ID_INVALID_PREFIX", c.prefix)
		}
		if !errs.HasCode(err, CodeIDInvalidPrefix) {
			t.Errorf("validateTypePrefix(%q) = %v, want code %s", c.prefix, err, CodeIDInvalidPrefix)
		}
		if got := fieldValue(err, "rule"); got != c.rule {
			t.Errorf("validateTypePrefix(%q) rule = %q, want %q", c.prefix, got, c.rule)
		}
		//: the refusal must never carry the prefix itself — it is unbounded
		//: caller input and these errors are logged.
		for _, f := range errs.FieldsOf(err) {
			if f.StringValue() == c.prefix && c.prefix != "" {
				t.Errorf("validateTypePrefix(%q) echoed the prefix in field %q", c.prefix, f.Key())
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_crockford32_roundtrip pins that the decoder is the exact inverse of the
// encoder over BOTH alphabets. The two schemes share one bit packing, so a
// defect here would corrupt ULID and TypeID together.
func Test_crockford32_roundtrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		alphabet string
	}
	tests := []tc{
		{"the lowercase TypeID alphabet", typeIDAlphabet},
		{"the uppercase ULID alphabet", ulidAlphabet},
	}
	var zeros, ones, ascending [uuidRawLen]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	for i := range ascending {
		ascending[i] = byte(i * 17)
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for _, raw := range [][uuidRawLen]byte{zeros, ones, ascending} {
			encoded := crockford32Encode(raw[:], c.alphabet)
			decoded, err := crockford32Decode(encoded, c.alphabet)
			if err != nil {
				t.Fatalf("crockford32Decode(%q) = %v, want nil", encoded, err)
			}
			if decoded != raw {
				t.Errorf("round trip changed the value: %x -> %q -> %x", raw, encoded, decoded)
			}
			if again := crockford32Encode(decoded[:], c.alphabet); again != encoded {
				t.Errorf("round trip changed the rendering: %q -> %x -> %q", encoded, decoded, again)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_crockford32Decode_rejects pins the three refusals.
//
// The pad-bit one is the subtle case and the reason the check exists: 26
// characters carry 130 bits while the value is 128, so a first character above
// '7' encodes a number no 16-byte value can hold. Accepting it would silently
// drop the two high bits and make two distinct strings decode identically —
// the decoder would stop being the encoder's inverse without ever erroring.
func Test_crockford32Decode_rejects(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		rule string
	}
	tests := []tc{
		{"the empty string", "", "length"},
		{"one character short", strings.Repeat("0", crockfordChars-1), "length"},
		{"one character long", strings.Repeat("0", crockfordChars+1), "length"},
		//: i, l, o and u are excluded from Crockford precisely because a human
		//: transcribing an identifier confuses them with 1, 1, 0 and v.
		{"the excluded letter i", strings.Repeat("0", crockfordChars-1) + "i", "alphabet"},
		{"the excluded letter l", strings.Repeat("0", crockfordChars-1) + "l", "alphabet"},
		{"the excluded letter o", strings.Repeat("0", crockfordChars-1) + "o", "alphabet"},
		{"the excluded letter u", strings.Repeat("0", crockfordChars-1) + "u", "alphabet"},
		{"an uppercase symbol", strings.Repeat("0", crockfordChars-1) + "Z", "alphabet"},
		{"a hyphen", strings.Repeat("0", crockfordChars-1) + "-", "alphabet"},
		//: '8' is the first value whose top pad bit is set.
		{"the first over-wide value", "8" + strings.Repeat("0", crockfordChars-1), "overflow"},
		{"the largest over-wide value", strings.Repeat("z", crockfordChars), "overflow"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		_, err := crockford32Decode(c.in, typeIDAlphabet)
		if err == nil {
			t.Fatalf("crockford32Decode(%q) = nil error, want ID_MALFORMED", c.in)
		}
		if !errs.HasCode(err, CodeIDMalformed) {
			t.Errorf("crockford32Decode(%q) = %v, want code %s", c.in, err, CodeIDMalformed)
		}
		if got := fieldValue(err, "rule"); got != c.rule {
			t.Errorf("crockford32Decode(%q) rule = %q, want %q", c.in, got, c.rule)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: '7' is the largest first character that still fits 128 bits — the
	//: boundary the overflow cases sit one step above.
	widest := "7" + strings.Repeat("z", crockfordChars-1)
	got, err := crockford32Decode(widest, typeIDAlphabet)
	if err != nil {
		t.Fatalf("crockford32Decode(%q) = %v, want nil", widest, err)
	}
	for i, b := range got {
		if b != 0xFF {
			t.Fatalf("crockford32Decode(%q) byte %d = %#x, want 0xFF", widest, i, b)
		}
	}
}

// Test_parseUUID pins the inverse of formatUUID, including the separator
// positions. A parser that accepts dashes anywhere would silently accept
// re-grouped identifiers that no other implementation renders.
func Test_parseUUID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		rule string
	}
	tests := []tc{
		{"a canonical UUID", "00010203-0405-0607-0809-0a0b0c0d0e0f", ""},
		{"all zeros", "00000000-0000-0000-0000-000000000000", ""},
		{"all ones", "ffffffff-ffff-ffff-ffff-ffffffffffff", ""},
		{"the empty string", "", "uuid_length"},
		{"the undashed form", "000102030405060708090a0b0c0d0e0f", "uuid_length"},
		{"a misplaced dash", "000102-030405-0607-0809-0a0b0c0d0e0f", "uuid_separator"},
		{"an underscore separator", "00010203_0405-0607-0809-0a0b0c0d0e0f", "uuid_separator"},
		{"a non-hex digit", "0001020g-0405-0607-0809-0a0b0c0d0e0f", "uuid_alphabet"},
		{"an uppercase hex digit is accepted", "00010203-0405-0607-0809-0A0B0C0D0E0F", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		raw, err := parseUUID(c.in)
		if c.rule == "" {
			if err != nil {
				t.Fatalf("parseUUID(%q) = %v, want nil", c.in, err)
			}
			//: re-rendering must reproduce the input, lowercased — formatUUID
			//: emits lower case, which is the canonical rendering.
			if got := formatUUID(raw[:]); got != strings.ToLower(c.in) {
				t.Errorf("parseUUID/formatUUID round trip gave %q, want %q", got, strings.ToLower(c.in))
			}
			return
		}
		if err == nil {
			t.Fatalf("parseUUID(%q) = nil error, want ID_MALFORMED", c.in)
		}
		if !errs.HasCode(err, CodeIDMalformed) {
			t.Errorf("parseUUID(%q) = %v, want code %s", c.in, err, CodeIDMalformed)
		}
		if got := fieldValue(err, "rule"); got != c.rule {
			t.Errorf("parseUUID(%q) rule = %q, want %q", c.in, got, c.rule)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
