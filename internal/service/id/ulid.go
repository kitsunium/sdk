// Package id — ULID generator (48-bit time + 80-bit random, Crockford base32).
package id

import (
	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// ulidAlphabet is Crockford base32 (excludes I, L, O, U) in the UPPER case ULID
// renders with — TypeID uses the same 32 symbols in lower case. The rendering
// length is crockfordChars: a ULID is exactly the 128-bit Crockford packing
// TypeID also uses, which is why both go through crockford32Encode.
const ulidAlphabet string = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ULID is the registered ULID generator: a 48-bit ms timestamp + 80 random bits
// rendered as 26 Crockford-base32 chars, lexically sortable by time.
var ULID = coreid.Register(ulidGen{})

// ulidGen prefixes a millisecond timestamp then fills the remainder randomly.
type ulidGen struct{}

// Scheme implements core/id.Generator.
func (ulidGen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "ulid"
}

// New builds the 16-byte time+random form and renders it Crockford base32.
func (ulidGen) New() (newID string, err error) {
	//: 16 raw bytes back the 128-bit identifier.
	var b [uuidRawLen]byte
	//: the leading 6 bytes carry the Unix-millisecond timestamp (big-endian).
	putUint48BE(b[:], clock.System.Now().UnixMilli())
	//: the remaining 10 bytes are secure random entropy.
	if rerr := readRandom(b[tsBytes:]); rerr != nil {
		//: propagate the wrapped entropy failure.
		return "", rerr
	}
	//: render the canonical 26-char Crockford base32 form.
	return crockford32(b[:]), nil
}

// crockford32 renders the 16-byte big-endian value b as crockfordChars uppercase
// Crockford base32 characters. The bit packing lives in crockford32Encode,
// shared with TypeID.
func crockford32(b []byte) string {
	//: ULID is the uppercase rendering of the shared 128-bit packing.
	return crockford32Encode(b, ulidAlphabet)
}
