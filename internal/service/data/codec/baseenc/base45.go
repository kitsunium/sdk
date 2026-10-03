// Package baseenc — Base45 (RFC 9285) alphabet codec. Unlike base16/32/64
// and ascii85, the stdlib ships no base45 encoder, so the encode/decode
// transforms live here. Base45 is a block encoding (base45PairBytes input
// bytes → base45GroupChars output chars; a trailing byte → base45TailChars
// chars), hence O(n) — safe to share the 10 MiB maxBaseEncBytes cap with
// the stdlib-backed variants.
package baseenc

// base45Alphabet is the RFC 9285 §3 table: indices 0..44 map to these
// runes. All printable ASCII, QR-code "alphanumeric mode" safe.
const base45Alphabet string = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ $%*+-./:"

const (
	// base45Radix is the alphabet size; a byte pair encodes into base 45³.
	base45Radix int = 45
	// base45RadixSq is base45Radix² — the place value of the third digit.
	base45RadixSq int = base45Radix * base45Radix
	// base45GroupChars is the encoded char count for one input byte-pair.
	base45GroupChars int = 3
	// base45TailChars is the encoded char count for a trailing single byte.
	base45TailChars int = 2
	// base45PairBytes is the input byte count per encoded 3-char group.
	base45PairBytes int = 2
	// base45HighShift is the bit shift isolating the high byte of a pair.
	base45HighShift int = 8
	// base45TableSize is the reverse-lookup table span — every byte value.
	base45TableSize int = 256
	// base45NotMember marks a byte absent from the alphabet in the table.
	base45NotMember int = -1
	// base45MaxPair is the largest value a 3-char group may decode to: a
	// byte pair is 0..0xFFFF, so any triple above this is malformed input.
	base45MaxPair int = 0xFFFF
	// base45MaxSingle is the largest value a trailing 2-char group may
	// decode to: a single byte is 0..0xFF.
	base45MaxSingle int = 0xFF
)

// base45Reverse maps an ASCII byte to its alphabet index, or base45NotMember
// when the byte is not a member of the Base45 alphabet. Built once at load.
var base45Reverse = buildBase45Reverse()

// buildBase45Reverse returns the reverse-lookup table for the Base45
// alphabet; non-member bytes map to base45NotMember.
func buildBase45Reverse() [base45TableSize]int {
	//: start every byte as "not a member".
	var table [base45TableSize]int
	//: seed the whole table with the sentinel.
	for i := range table {
		//: base45NotMember is what the decoder checks for an illegal char.
		table[i] = base45NotMember
	}
	//: record each alphabet rune's index (alphabet is ASCII).
	for i := range base45Alphabet {
		//: the byte value indexes the table directly.
		table[base45Alphabet[i]] = i
	}
	//: hand back the populated table.
	return table
}

// encodeBase45 returns the RFC 9285 encoding of raw. Two input bytes become
// three output chars (little-endian base-45 of the 16-bit pair); a trailing
// odd byte becomes two chars.
func encodeBase45(raw []byte) []byte {
	//: 3 chars per even pair + 2 for a trailing byte; sized up-front.
	out := make([]byte, 0, (len(raw)/base45PairBytes)*base45GroupChars+base45TailChars)
	i := 0
	//: consume whole byte pairs first.
	for ; i+1 < len(raw); i += base45PairBytes {
		//: 16-bit big-endian value of the pair.
		n := int(raw[i])<<base45HighShift | int(raw[i+1])
		//: three little-endian base-45 digits.
		out = append(out, base45Alphabet[n%base45Radix])
		out = append(out, base45Alphabet[(n/base45Radix)%base45Radix])
		out = append(out, base45Alphabet[n/base45RadixSq])
	}
	//: a trailing odd byte encodes to two digits.
	if i < len(raw) {
		//: single byte value 0..255.
		n := int(raw[i])
		out = append(out, base45Alphabet[n%base45Radix])
		out = append(out, base45Alphabet[n/base45Radix])
	}
	//: hand back the encoded bytes.
	return out
}

// decodeBase45 reverses encodeBase45. ok is false when the input length
// class is illegal (len%3 == 1), a char is outside the alphabet, or a
// group's value exceeds the byte(s) it must represent.
func decodeBase45(s []byte) (raw []byte, ok bool) {
	//: len%3 of 1 is impossible — groups are 3 (pair) or 2 (single) chars.
	if len(s)%base45GroupChars == 1 {
		//: malformed length class.
		return nil, false
	}
	//: 2 bytes per triple + 1 for a trailing pair.
	out := make([]byte, 0, (len(s)/base45GroupChars)*base45PairBytes+1)
	i := 0
	//: decode whole 3-char groups into byte pairs.
	for ; i+base45TailChars < len(s); i += base45GroupChars {
		//: reconstruct the 16-bit value; reject out-of-range groups.
		n, valOK := base45Triple(s[i], s[i+1], s[i+base45GroupChars-1])
		//: illegal char or value too large for two bytes.
		if !valOK || n > base45MaxPair {
			//: reject the whole input.
			return nil, false
		}
		//: big-endian split back into two bytes (byte() truncates the low 8).
		out = append(out, byte(n>>base45HighShift), byte(n))
	}
	//: a trailing 2-char group decodes to a single byte.
	if i < len(s) {
		//: reconstruct and range-check the single byte.
		n, valOK := base45Pair(s[i], s[i+1])
		//: illegal char or value too large for one byte.
		if !valOK || n > base45MaxSingle {
			//: reject the whole input.
			return nil, false
		}
		//: append the recovered byte.
		out = append(out, byte(n))
	}
	//: success — out holds the decoded bytes.
	return out, true
}

// base45Triple decodes three alphabet chars into their combined value, or
// reports ok=false when any char is outside the alphabet.
func base45Triple(c0, c1, c2 byte) (n int, ok bool) {
	//: reverse-lookup each digit; base45NotMember means "illegal char".
	d0, d1, d2 := base45Reverse[c0], base45Reverse[c1], base45Reverse[c2]
	//: any illegal digit fails the group.
	if d0 == base45NotMember || d1 == base45NotMember || d2 == base45NotMember {
		//: reject.
		return 0, false
	}
	//: little-endian base-45 reassembly.
	return d0 + d1*base45Radix + d2*base45RadixSq, true
}

// base45Pair decodes two alphabet chars into their combined value, or
// reports ok=false when either char is outside the alphabet.
func base45Pair(c0, c1 byte) (n int, ok bool) {
	//: reverse-lookup each digit.
	d0, d1 := base45Reverse[c0], base45Reverse[c1]
	//: either illegal digit fails the group.
	if d0 == base45NotMember || d1 == base45NotMember {
		//: reject.
		return 0, false
	}
	//: little-endian base-45 reassembly.
	return d0 + d1*base45Radix, true
}
