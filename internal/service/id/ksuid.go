// Package id — KSUID generator (32-bit second prefix + 128-bit payload, base62).
package id

import (
	"encoding/binary"
	"math"
	"strings"
	"time"

	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// ksuidEpoch is the KSUID epoch in Unix seconds (2014-05-13T16:53:20Z). The
	// format shifts its origin forward so the 32-bit counter buys ~136 years
	// from a useful date instead of burning 44 of them on 1970..2014.
	ksuidEpoch int64 = 1_400_000_000
	// ksuidTimeBytes is the width of the second-resolution timestamp prefix.
	ksuidTimeBytes int = 4
	// ksuidPayloadBytes is the width of the random payload.
	ksuidPayloadBytes int = 16
	// ksuidRawLen is the decoded width: 4 + 16 = 20 bytes (160 bits).
	ksuidRawLen int = ksuidTimeBytes + ksuidPayloadBytes
	// ksuidChars is the canonical rendering length: a 160-bit value needs
	// ceil(160 / log2(62)) = 27 base62 characters.
	ksuidChars int = 27
	// base62Alphabet is the KSUID symbol set. The order is deliberate: it is
	// ascending ASCII, so a fixed-width base62 rendering sorts lexicographically
	// exactly as the underlying integer does — which is what makes a KSUID
	// string sortable by creation time.
	base62Alphabet string = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	// base62Radix is the length of base62Alphabet, named for the arithmetic.
	base62Radix int = 62
)

// KSUID is the registered KSUID generator: a 32-bit second-resolution timestamp
// followed by 128 random bits, rendered as 27 base62 characters that sort by
// creation time as plain strings.
var KSUID = coreid.Register(ksuidGen{})

// ksuidGen prefixes a second-resolution timestamp then fills the remainder
// randomly. Stateless — the singleton is safe to share.
type ksuidGen struct{}

// Scheme implements core/id.Generator.
func (ksuidGen) Scheme() coreid.Scheme {
	//: the registered scheme key.
	return "ksuid"
}

// New builds the 20-byte time+random form and renders it base62.
func (ksuidGen) New() (newID string, err error) {
	//: 20 raw bytes back the 160-bit identifier.
	var b [ksuidRawLen]byte
	//: seconds since the KSUID epoch, which must fit the 32-bit prefix.
	secs := clock.System.Now().Unix() - ksuidEpoch
	//: a clock before 2014 or past 2150 cannot be represented; encoding it
	//: anyway would wrap the prefix and silently destroy the sort order.
	if secs < 0 || secs > math.MaxUint32 {
		//: refuse rather than mint an id whose timestamp lies.
		return "", TimestampRange
	}
	//: the leading 4 bytes carry the epoch-relative seconds (big-endian).
	binary.BigEndian.PutUint32(b[:ksuidTimeBytes], uint32(secs))
	//: the remaining 16 bytes are secure random entropy.
	if rerr := readRandom(b[ksuidTimeBytes:]); rerr != nil {
		//: propagate the wrapped entropy failure.
		return "", rerr
	}
	//: render the canonical 27-char base62 form.
	return base62Encode(b[:]), nil
}

// ParseKSUID decodes the canonical 27-character rendering s, returning the
// instant the identifier was issued (second resolution, UTC) and its 16-byte
// random payload. It is the inverse of what KSUID.New renders.
func ParseKSUID(s string) (issued time.Time, payload [ksuidPayloadBytes]byte, err error) {
	//: decode the base62 rendering back to the 20 raw bytes.
	raw, decErr := base62Decode(s)
	//: a malformed rendering is reported with the rule that rejected it.
	if decErr != nil {
		//: propagate the typed rejection unchanged.
		return time.Time{}, payload, decErr
	}
	//: the leading 4 bytes are the epoch-relative second counter.
	secs := int64(binary.BigEndian.Uint32(raw[:ksuidTimeBytes])) + ksuidEpoch
	//: the trailing 16 bytes are the payload, copied out of the scratch array.
	copy(payload[:], raw[ksuidTimeBytes:])
	//: UTC so the value is comparable regardless of the reader's zone.
	return time.Unix(secs, 0).UTC(), payload, nil
}

// base62Encode renders the ksuidRawLen-byte big-endian value b as exactly
// ksuidChars base62 characters, left-padded with the zero symbol.
//
// The padding is not cosmetic: a variable-width rendering would sort "9" after
// "10", so every KSUID is rendered to the same width and the string order then
// matches the numeric order.
func base62Encode(b []byte) string {
	//: long division consumes its input, so work on a scratch copy.
	var work [ksuidRawLen]byte
	//: seed the scratch with the value to divide.
	copy(work[:], b)
	//: the rendered characters, filled back-to-front.
	var out [ksuidChars]byte
	//: each round divides the whole 160-bit value by 62; the remainder is the
	//: least-significant digit not yet emitted.
	for i := ksuidChars - 1; i >= 0; i-- {
		//: carry of the schoolbook division, always < base62Radix.
		rem := 0
		//: divide the big-endian digits from the most-significant byte down.
		for j := range work {
			//: the running dividend for this byte position.
			cur := rem<<bitsPerByte | int(work[j])
			//: the quotient byte stays in place for the next round.
			work[j] = byte(cur / base62Radix)
			//: the remainder feeds the next, less-significant byte.
			rem = cur % base62Radix
		}
		//: the final remainder is this round's base62 digit.
		out[i] = base62Alphabet[rem]
	}
	//: the assembled 27-char rendering.
	return string(out[:])
}

// base62Decode reverses base62Encode, returning the ksuidRawLen raw bytes s
// encodes. It rejects a wrong length, a symbol outside the alphabet, and a
// value too large for 160 bits.
//
// That last rejection is load-bearing: 62^27 is about 2^160.7, so roughly a
// third of the well-formed-looking 27-character strings encode a number no
// KSUID can hold. Truncating them would make distinct strings decode to the
// same identifier.
func base62Decode(s string) (raw [ksuidRawLen]byte, err error) {
	//: the rendering is fixed-width; anything else is not this encoding.
	if len(s) != ksuidChars {
		//: name the rule that rejected it without echoing the input.
		return raw, errs.Wrap(Malformed, errs.WrapParams{},
			errs.String("rule", "length"), errs.Int("length", len(s)))
	}
	//: accumulate raw = raw*62 + digit, most-significant digit first.
	for i := range ksuidChars {
		//: resolve the symbol to its value; -1 means it is not a symbol.
		digit := strings.IndexByte(base62Alphabet, s[i])
		//: a character outside the alphabet disqualifies the whole string.
		if digit < 0 {
			//: name the rule and the offset, never the character itself.
			return raw, errs.Wrap(Malformed, errs.WrapParams{},
				errs.String("rule", "alphabet"), errs.Int("offset", i))
		}
		//: the incoming digit is the carry into the least-significant byte.
		carry := digit
		//: schoolbook multiply-add, from the least-significant byte upward.
		for j := ksuidRawLen - 1; j >= 0; j-- {
			//: widen before multiplying so the carry is not lost.
			cur := int(raw[j])*base62Radix + carry
			//: keep the low byte in place.
			raw[j] = byte(cur)
			//: propagate the rest to the next, more-significant byte.
			carry = cur >> bitsPerByte
		}
		//: a carry out of the top byte means the value exceeds 160 bits.
		if carry != 0 {
			//: refuse rather than truncate — truncation would alias two inputs.
			return raw, errs.Wrap(Malformed, errs.WrapParams{},
				errs.String("rule", "overflow"))
		}
	}
	//: the fully reconstructed 160-bit value.
	return raw, nil
}
