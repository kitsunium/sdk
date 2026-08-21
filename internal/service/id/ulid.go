// Package id — ULID generator (48-bit time + 80-bit random, Crockford base32).
package id

import (
	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

const (
	// ulidChars is the canonical ULID rendering length.
	ulidChars int = 26
	// bitsPerChar is the base32 group width (5 bits per Crockford char).
	bitsPerChar int = 5
	// ulidPadBits is the zero pad at the MSB: 26*5 - 16*8 = 130-128 = 2.
	ulidPadBits int = ulidChars*bitsPerChar - uuidRawLen*bitsPerByte
	// ulidAlphabet is Crockford base32 (excludes I, L, O, U).
	ulidAlphabet string = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

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

// crockford32 renders the 16-byte big-endian value b as ulidChars Crockford
// base32 characters, MSB-first with ulidPadBits zero pad bits at the top.
func crockford32(b []byte) string {
	//: one output byte per base32 character.
	var out [ulidChars]byte
	//: walk each output character, most-significant group first.
	for i := range ulidChars {
		//: accumulate bitsPerChar bits into v, MSB-first.
		v := 0
		//: pull each of the 5 bits for this character.
		for j := range bitsPerChar {
			//: position within the zero-padded 130-bit space, then de-pad.
			realIdx := i*bitsPerChar + j - ulidPadBits
			//: pad bits (realIdx < 0) contribute a zero high bit.
			bit := 0
			//: real bits read MSB-first from the byte array.
			if realIdx >= 0 {
				bit = int((b[realIdx/bitsPerByte] >> (bitsPerByte - 1 - realIdx%bitsPerByte)) & 1)
			}
			//: shift the accumulator and OR in this bit.
			v = v<<1 | bit
		}
		//: map the 5-bit group to its Crockford character.
		out[i] = ulidAlphabet[v]
	}
	//: the assembled 26-char rendering.
	return string(out[:])
}
