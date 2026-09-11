package session_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/session"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sampleRaw is a deterministic 32-byte identifier body. It is a test fixture,
// never a key: nothing here is secret, which is exactly why it can be written
// down.
func sampleRaw() []byte {
	//: 0x00..0x1f, so a truncation shows up as a visibly different value.
	raw := make([]byte, session.IDLen)
	for i := range raw {
		raw[i] = byte(i)
	}
	return raw
}

// TestNewIDRefusesEveryLengthButOne pins that an identifier is never reshaped
// to fit. A truncated identifier is a weaker secret that would still work,
// which is the failure mode worth refusing at construction.
func TestNewIDRefusesEveryLengthButOne(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		size int
		want bool
	}{
		{"empty", 0, false},
		{"one byte short", session.IDLen - 1, false},
		{"one byte long", session.IDLen + 1, false},
		{"exact", session.IDLen, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := session.NewID(make([]byte, tc.size))
			//: the accepted length is the only one that yields an identifier.
			if tc.want {
				if err != nil || id.IsZero() {
					t.Fatalf("NewID(%d bytes) = (%v, %v), want a usable ID", tc.size, id, err)
				}
				return
			}
			if !errs.HasCode(err, session.CodeInvalidID) {
				t.Errorf("NewID(%d bytes) = %v, want CodeInvalidID", tc.size, err)
			}
			if !id.IsZero() {
				t.Errorf("NewID(%d bytes) returned a non-zero ID alongside its error", tc.size)
			}
		})
	}
}

// TestNewIDCopiesItsInput pins that the caller's buffer is not the ID's
// storage: a caller who zeroes or reuses the slice must not silently change
// every session minted from it.
func TestNewIDCopiesItsInput(t *testing.T) {
	t.Parallel()
	raw := sampleRaw()
	id, err := session.NewID(raw)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	before := id.Reveal()
	//: the caller wipes their buffer, as anyone handling key material should.
	clear(raw)
	if id.Reveal() != before {
		t.Errorf("mutating the caller's slice changed the ID: %q -> %q", before, id.Reveal())
	}
}

// TestParseIDRoundTripsRevealAndRefusesEverythingElse pins the inbound path:
// exactly one spelling of an identifier is accepted, so two encodings of one
// session cannot both work.
func TestParseIDRoundTripsRevealAndRefusesEverythingElse(t *testing.T) {
	t.Parallel()
	canonical, err := session.NewID(sampleRaw())
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	//: padded base64url is a DIFFERENT spelling of the same bytes, and it is
	//: refused: accepting both would give one session two identifiers.
	padded := base64.URLEncoding.EncodeToString(sampleRaw())
	tests := []struct {
		name    string
		encoded string
		accept  bool
	}{
		{"canonical", canonical.Reveal(), true},
		{"padded", padded, false},
		{"standard alphabet", base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0xFF}, session.IDLen)), false},
		{"hex", hex.EncodeToString(sampleRaw()), false},
		{"empty", "", false},
		{"truncated", canonical.Reveal()[:10], false},
		{"not base64", "not a session identifier!!", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, parseErr := session.ParseID(tc.encoded)
			//: the canonical form must round-trip to the same identifier.
			if tc.accept {
				if parseErr != nil || !id.Equal(canonical) {
					t.Fatalf("ParseID(canonical) = (%v, %v), want the same ID", id, parseErr)
				}
				return
			}
			if !errs.HasCode(parseErr, session.CodeInvalidID) {
				t.Errorf("ParseID(%q) = %v, want CodeInvalidID", tc.encoded, parseErr)
			}
		})
	}
}

// TestAnIdentifierHasExactlyOneSpelling pins the claim ParseID makes — "two
// spellings of one identifier cannot exist" — at the one place base64 lets
// them exist. An identifier is session.IDLen bytes, 256 bits, and the 43
// characters that spell it carry 258, so the last character holds two bits no
// byte uses. A lenient decoder ignores them, and four different cookies then
// name one session; ADR 0042 refuses the same thing for tokens for the same
// reason.
//
// The respelled value is first proved to decode to the SAME bytes under the
// lenient decoder, so the refusal below cannot pass merely because the input
// was garbage.
//
// Mutation: decoding with base64.RawURLEncoding instead of its Strict() form
// failed with `ParseID(a second spelling of the same identifier) = (<redacted>,
// <nil>), want CodeInvalidID`.
func TestAnIdentifierHasExactlyOneSpelling(t *testing.T) {
	t.Parallel()
	canonical := mustID(t, sampleRaw()).Reveal()
	respelled := setUnusedLowBit(t, canonical)
	lenient, err := base64.RawURLEncoding.DecodeString(respelled)
	if err != nil || !bytes.Equal(lenient, sampleRaw()) {
		t.Fatalf("the respelling is not the same identifier under a lenient decoder (%v); the test would prove nothing", err)
	}
	id, parseErr := session.ParseID(respelled)
	if !errs.HasCode(parseErr, session.CodeInvalidID) {
		t.Fatalf("ParseID(a second spelling of the same identifier) = (%v, %v), want CodeInvalidID", id, parseErr)
	}
	if !id.IsZero() {
		t.Error("ParseID returned an identifier alongside its refusal")
	}
}

// setUnusedLowBit sets the lowest bit of the final character of an unpadded
// base64url string. That bit carries no data whenever the length is not a
// multiple of four, so the result is another spelling of the same bytes.
func setUnusedLowBit(t *testing.T, encoded string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	//: a length that is a multiple of four has no spare bits to set.
	if len(encoded)%4 == 0 {
		t.Fatalf("a %d-character encoding has no unused bits", len(encoded))
	}
	last := strings.IndexByte(alphabet, encoded[len(encoded)-1])
	//: a canonical encoding leaves the spare bits clear; one already set means
	//: the input was not canonical and there is nothing to respell.
	if last&1 == 1 {
		t.Fatalf("the final character of %q already has its low bit set", encoded)
	}
	return encoded[:len(encoded)-1] + string(alphabet[last|1])
}

// TestRevealIsCookieSafe pins that the canonical form needs no escaping to be
// used verbatim as a cookie value — the whole reason it is base64URL and
// unpadded.
func TestRevealIsCookieSafe(t *testing.T) {
	t.Parallel()
	id, err := session.NewID(bytes.Repeat([]byte{0xFF}, session.IDLen))
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	revealed := id.Reveal()
	//: 32 bytes in base64 is ceil(32/3)*4 - padding = 43 characters.
	if len(revealed) != 43 {
		t.Errorf("Reveal() length = %d, want 43", len(revealed))
	}
	//: RFC 6265 cookie-octet excludes these; base64url and the '=' padding are
	//: the two things that could have gone wrong.
	if strings.ContainsAny(revealed, "=+/ ;,\"\\") {
		t.Errorf("Reveal() = %q, which is not a bare cookie value", revealed)
	}
}

// TestDigestIsTheUnkeyedSHA256 pins the value a store indexes and names files
// by. It is asserted against an independently computed digest rather than
// against itself, so a change to the algorithm fails here rather than silently
// invalidating every stored record.
func TestDigestIsTheUnkeyedSHA256(t *testing.T) {
	t.Parallel()
	id, err := session.NewID(sampleRaw())
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	sum := sha256.Sum256(sampleRaw())
	want := hex.EncodeToString(sum[:])
	if id.Digest() != want {
		t.Errorf("Digest() = %q, want %q", id.Digest(), want)
	}
	//: 64 hex characters: filename-safe on every OS, including the
	//: case-insensitive ones.
	if len(id.Digest()) != 64 {
		t.Errorf("Digest() length = %d, want 64", len(id.Digest()))
	}
	//: the digest must not be the identifier.
	if strings.Contains(id.Digest(), id.Reveal()) {
		t.Error("Digest() contains the identifier verbatim")
	}
	//: a zero ID has no digest at all, so every zero ID does NOT share one
	//: valid-looking lookup key.
	if (session.ID{}).Digest() != "" {
		t.Errorf("zero ID Digest() = %q, want empty", (session.ID{}).Digest())
	}
}

// TestEqualIsTotalAndSelfConsistent pins the comparison every store routes
// through. The constant-time property itself is not timeable in a unit test;
// what is testable — and what a refactor would break — is that Equal is the
// only comparison and that it answers correctly at every length.
func TestEqualIsTotalAndSelfConsistent(t *testing.T) {
	t.Parallel()
	first, err := session.NewID(sampleRaw())
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	other := sampleRaw()
	//: differ in the LAST byte, which a prefix comparison would miss.
	other[session.IDLen-1] ^= 0xFF
	second, err := session.NewID(other)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	cases := map[string]bool{
		"self":            first.Equal(first),
		"same bytes":      first.Equal(mustID(t, sampleRaw())),
		"last byte flip":  first.Equal(second),
		"zero vs real":    (session.ID{}).Equal(first),
		"real vs zero":    first.Equal(session.ID{}),
		"zero vs zero":    (session.ID{}).Equal(session.ID{}),
		"reversed argord": second.Equal(first),
	}
	want := map[string]bool{
		"self": true, "same bytes": true, "last byte flip": false,
		"zero vs real": false, "real vs zero": false,
		"zero vs zero": true, "reversed argord": false,
	}
	for name, got := range cases {
		if got != want[name] {
			t.Errorf("Equal(%s) = %v, want %v", name, got, want[name])
		}
	}
}

// TestIDNeverRendersItself is the regression guard for the claim that an
// identifier cannot reach a log line by accident. Every fmt verb a struct dump
// or a log line could use is checked, because String alone does not cover %#v.
func TestIDNeverRendersItself(t *testing.T) {
	t.Parallel()
	id, err := session.NewID(sampleRaw())
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	secret := id.Reveal()
	verbs := []string{"%v", "%s", "%#v", "%+v", "%q"}
	for _, verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			rendered := fmt.Sprintf(verb, id)
			if strings.Contains(rendered, secret) {
				t.Fatalf("%s rendered the identifier: %s", verb, rendered)
			}
			//: and it must not leak the raw bytes another way either.
			if strings.Contains(rendered, "0x") || strings.Contains(rendered, "[") {
				t.Errorf("%s = %s, want a redaction marker", verb, rendered)
			}
		})
	}
	//: the same guard one level up: an ID inside a struct must stay redacted.
	holder := struct{ Session session.ID }{Session: id}
	if strings.Contains(fmt.Sprintf("%+v", holder), secret) {
		t.Error("an ID embedded in a struct rendered its identifier")
	}
}

// mustID builds an ID or fails the test.
func mustID(t *testing.T, raw []byte) session.ID {
	t.Helper()
	id, err := session.NewID(raw)
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id
}
