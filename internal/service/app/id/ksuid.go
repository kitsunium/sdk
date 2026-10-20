package id

import (
	"crypto/rand"
	"encoding/binary"
	"math"
	"time"

	coreid "github.com/kitsunium/sdk/internal/core/app/id"
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
	// base62ReverseLen is the width of the decode table: one entry per possible
	// byte value, so a lookup is an index and never a bounds decision.
	base62ReverseLen int = 256
	// base62Radix is the length of base62Alphabet, named for the arithmetic.
	base62Radix int = 62
	// ksuidWordBytes is the width of one word of base62Encode's division.
	ksuidWordBytes int = 4
	// ksuidWords is the raw value's width in 32-bit words: 20 / 4 = 5.
	ksuidWords int = ksuidRawLen / ksuidWordBytes
	// bitsPerWord is the width of one division word, in bits.
	bitsPerWord int = ksuidWordBytes * bitsPerByte
	// base62DigitsPerRound is how many base-62 digits one division yields.
	base62DigitsPerRound int = 5
	// base62Pow5 is 62^5 = 916 132 832, the largest power of 62 below 2^32:
	// the divisor of one round, whose remainder is five digits.
	base62Pow5 uint64 = 62 * 62 * 62 * 62 * 62
)

// KSUID is the registered KSUID generator: a 32-bit second-resolution timestamp
// followed by 128 random bits, rendered as 27 base62 characters that sort by
// creation time as plain strings.
var (
	// KSUID is the registered generator.
	KSUID = coreid.Register(ksuidGen{})
	// base62Reverse is the decode table; see buildBase62Reverse for why it is
	// derived rather than written out.
	base62Reverse = buildBase62Reverse()
)

// ksuidGen prefixes a second-resolution timestamp then fills the remainder
// randomly. Stateless — the singleton is safe to share.
type ksuidGen struct{}

// Scheme implements core/app/id.Generator.
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
		return "", coreid.TimestampRange
	}
	//: the leading 4 bytes carry the epoch-relative seconds (big-endian).
	binary.BigEndian.PutUint32(b[:ksuidTimeBytes], uint32(secs))
	//: the remaining 16 bytes are secure random entropy; crypto/rand.Read
	//: cannot fail (see the package doc).
	_, _ = rand.Read(b[ksuidTimeBytes:])
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
	//: long division consumes its input, so work on a scratch copy — as five
	//: big-endian 32-bit words rather than twenty bytes, so one division step
	//: moves four bytes instead of one.
	var work [ksuidWords]uint32
	//: seed the scratch with the value to divide.
	for j := range work {
		//: word j holds bytes 4j..4j+3, most significant first.
		work[j] = binary.BigEndian.Uint32(b[j*ksuidWordBytes:])
	}
	//: the rendered characters, filled back-to-front.
	var out [ksuidChars]byte
	//: each round divides the whole 160-bit value by 62^5 — the largest power
	//: of 62 below 2^32 — and the remainder holds the next five digits: six
	//: rounds of five word divisions, where dividing by 62 itself took 27
	//: rounds of twenty byte divisions.
	for i := ksuidChars; i > 0; {
		//: carry of the schoolbook division, always < base62Pow5.
		var rem uint64
		//: divide the big-endian words from the most-significant one down.
		for j := range work {
			//: the running dividend for this word position.
			cur := rem<<bitsPerWord | uint64(work[j])
			//: the quotient word stays in place for the next round.
			work[j] = uint32(cur / base62Pow5)
			//: the remainder feeds the next, less-significant word.
			rem = cur % base62Pow5
		}
		//: the remainder's base-62 digits, least significant first. The last
		//: round has room for two: 62^27 > 2^160, so the quotient left after
		//: five rounds is below 62^2 and the digits dropped here are zero.
		for k := 0; k < base62DigitsPerRound && i > 0; k++ {
			i--
			out[i] = base62Alphabet[rem%uint64(base62Radix)]
			rem /= uint64(base62Radix)
		}
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
		return raw, errs.Wrap(coreid.Malformed, errs.WrapParams{},
			errs.String("rule", "length"), errs.Int("length", len(s)))
	}
	//: accumulate value = value*62 + digit, most-significant digit first, in
	//: five 32-bit words (big-endian order) so one multiply-add step moves four
	//: bytes instead of one.
	var words [ksuidWords]uint32
	for i := range ksuidChars {
		//: resolve the symbol to its value; -1 means it is not a symbol. The
		//: table is a direct index rather than a scan of the alphabet: a
		//: strings.IndexByte here walked up to 62 bytes for each of the 27
		//: characters, and a CPU profile put that search at 13.7 % of the whole
		//: decode. It is DERIVED from base62Alphabet below, so the two cannot
		//: drift apart.
		digit := int(base62Reverse[s[i]])
		//: a character outside the alphabet disqualifies the whole string.
		if digit < 0 {
			//: name the rule and the offset, never the character itself.
			return raw, errs.Wrap(coreid.Malformed, errs.WrapParams{},
				errs.String("rule", "alphabet"), errs.Int("offset", i))
		}
		//: the incoming digit is the carry into the least-significant word.
		carry := uint64(digit)
		//: schoolbook multiply-add, from the least-significant word upward.
		for j := ksuidWords - 1; j >= 0; j-- {
			//: widen before multiplying so the carry is not lost.
			cur := uint64(words[j])*uint64(base62Radix) + carry
			//: keep the low word in place.
			words[j] = uint32(cur)
			//: propagate the rest to the next, more-significant word.
			carry = cur >> bitsPerWord
		}
		//: a carry out of the top word means the value exceeds 160 bits.
		if carry != 0 {
			//: refuse rather than truncate — truncation would alias two inputs.
			return raw, errs.Wrap(coreid.Malformed, errs.WrapParams{},
				errs.String("rule", "overflow"))
		}
	}
	//: the fully reconstructed 160-bit value, laid back out as bytes.
	for j, word := range words {
		//: word j is bytes 4j..4j+3, most significant first.
		binary.BigEndian.PutUint32(raw[j*ksuidWordBytes:], word)
	}
	//: the decoded identifier.
	return raw, nil
}

// base62Reverse maps a byte to its base62 digit value, or -1 when the byte is
// not a symbol of the alphabet. It exists so decoding is a table index instead
// of a linear search: strings.IndexByte scanned up to 62 bytes per character,
// 27 characters per identifier, which a CPU profile measured at 13.7 % of a
// ParseKSUID call.
//
// It is BUILT from base62Alphabet rather than written out, because a
// hand-transcribed table that disagreed with the alphabet would decode some
// identifiers to the wrong value and reject others as malformed, and nothing
// would say so.

// buildBase62Reverse derives the decode table from base62Alphabet.
func buildBase62Reverse() [base62ReverseLen]int8 {
	//: -1 everywhere means "not a symbol" until the alphabet says otherwise.
	var table [base62ReverseLen]int8
	//: fill the whole range first; only the 62 symbols are overwritten below.
	for i := range table {
		//: the sentinel the decoder tests against.
		table[i] = -1
	}
	//: each symbol's position IS its digit value, by definition of the encoding.
	for value := range len(base62Alphabet) {
		//: index by the symbol's byte so the lookup is a single load.
		table[base62Alphabet[value]] = int8(value)
	}
	//: the completed reverse mapping.
	return table
}
