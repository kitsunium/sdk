// Package baseenc — big-endian base-conversion arithmetic shared by the
// Base58 and Base62 variants. Unlike the block encodings, these treat the
// whole input as one integer and divide it down by the radix, so the
// transforms are O(n²) — callers MUST cap input at maxConvBytes first.
package baseenc

import "slices"

// byteBase is the radix of the input/output byte stream (one byte = base 256).
const byteBase int = 256

// buildReverseTable returns the byte→index reverse-lookup for alphabet;
// bytes absent from alphabet map to base45NotMember (the shared sentinel).
// Shared by Base58/Base62 (and structurally identical to Base45's table).
func buildReverseTable(alphabet string) [base45TableSize]int {
	//: start every byte as "not a member".
	var table [base45TableSize]int
	//: seed the whole table with the absent sentinel.
	for i := range table {
		//: base45NotMember is what decoders check for an illegal char.
		table[i] = base45NotMember
	}
	//: record each alphabet rune's index (alphabets are ASCII).
	for i := range alphabet {
		//: the byte value indexes the table directly.
		table[alphabet[i]] = i
	}
	//: hand back the populated table.
	return table
}

// divmodInPlace divides the big-endian magnitude buf by radix in place and
// returns the remainder. buf keeps its length (leading zeros appear); the
// caller trims them between divisions.
func divmodInPlace(buf []byte, radix int) int {
	rem := 0
	//: classic long division across the byte slice, most-significant first.
	for i := range buf {
		//: bring down the next byte onto the running remainder.
		acc := rem*byteBase + int(buf[i])
		//: quotient digit for this position.
		buf[i] = byte(acc / radix)
		//: carry the remainder to the next position.
		rem = acc % radix
	}
	//: the final remainder is the extracted base-`radix` digit.
	return rem
}

// trimLeadingZeros returns b without its leading zero bytes.
func trimLeadingZeros(b []byte) []byte {
	i := 0
	//: advance past every leading zero byte.
	for i < len(b) && b[i] == 0 {
		//: one more leading zero to drop.
		i++
	}
	//: slice off the zero prefix.
	return b[i:]
}

// mulAddWindow computes mag[start:] = mag[start:]*radix + add over the
// big-endian magnitude held in the TAIL of mag, and returns the new start
// index. A multiply that overflows the live window extends it leftwards
// into mag's reserved prefix instead of allocating a wider slice.
//
// The window can never run past index 0: an n-digit base-radix number is
// strictly below 256^n for every radix below 256, so len(s) bytes always
// hold the magnitude of an len(s)-digit input — which is exactly how
// decodeBaseN sizes mag.
//
// Growing by moving start rather than by prepending is what makes the
// decode path allocate once per call instead of once per new
// most-significant byte; a memory profile attributed 97.85 % of the
// package's allocated objects to the prepend this replaced. See BENCH.md.
func mulAddWindow(mag []byte, start, radix, add int) int {
	carry := add
	//: fold multiply+add through the live window, least-significant first.
	for i := len(mag) - 1; i >= start; i-- {
		//: byte() keeps the low 8 bits; the rest carries left.
		acc := int(mag[i])*radix + carry
		//: store the low byte, propagate the carry.
		mag[i] = byte(acc)
		carry = acc / byteBase
	}
	//: absorb the remaining carry into the reserved prefix (MSB-first).
	for carry > 0 {
		//: one more most-significant byte joins the window.
		start--
		//: low carry byte lands at the new high end.
		mag[start] = byte(carry)
		carry /= byteBase
	}
	//: hand back the (possibly widened) window start.
	return start
}

// encodeBaseN encodes raw as a big-endian base-conversion over alphabet
// (radix == len(alphabet)). Leading zero bytes become leading alphabet[0]
// chars. O(len(raw)²) — caller must cap input at maxConvBytes.
func encodeBaseN(raw []byte, alphabet string, radix int) []byte {
	//: count leading zero bytes — they encode as leading alphabet[0].
	zeros := 0
	//: scan the zero-byte prefix.
	for zeros < len(raw) && raw[zeros] == 0 {
		//: one more leading zero to reproduce.
		zeros++
	}
	//: working magnitude (divided down in place).
	buf := make([]byte, len(raw)-zeros)
	copy(buf, raw[zeros:])
	//: digits accumulate least-significant first.
	digits := make([]byte, 0, len(raw)*base45PairBytes)
	//: divide the magnitude down by the radix until it is exhausted.
	for len(buf) > 0 {
		//: next least-significant digit is the division remainder.
		digits = append(digits, alphabet[divmodInPlace(buf, radix)])
		//: drop the leading zero bytes the division produced.
		buf = trimLeadingZeros(buf)
	}
	//: assemble leading zero-chars + digits most-significant first.
	return assembleBaseN(alphabet[0], zeros, digits)
}

// assembleBaseN builds the final encoding: zeros copies of zeroChar followed
// by digits reversed (digits arrive least-significant first).
func assembleBaseN(zeroChar byte, zeros int, digits []byte) []byte {
	//: sized for the leading zero-chars plus every digit.
	out := make([]byte, 0, zeros+len(digits))
	//: emit one zeroChar per leading zero byte.
	for range zeros {
		//: leading zero byte → leading alphabet[0] char.
		out = append(out, zeroChar)
	}
	//: append digits (least-significant first), then flip that segment so
	//: the encoding reads most-significant first.
	out = append(out, digits...)
	slices.Reverse(out[zeros:])
	//: hand back the assembled encoding.
	return out
}

// decodeBaseN reverses encodeBaseN for alphabet/radix. ok is false when a
// char is outside the alphabet. Leading zero-chars restore leading zero
// bytes. O(len(s)²) — caller must cap input at maxConvBytes.
func decodeBaseN(s []byte, reverse *[base45TableSize]int, radix int) (raw []byte, ok bool) {
	//: count leading zero-digit chars (index 0) — they restore zero bytes.
	zeros := 0
	//: scan the zero-char prefix.
	for zeros < len(s) && reverse[s[zeros]] == 0 {
		//: one more leading zero byte to reproduce.
		zeros++
	}
	//: one allocation for the whole fold — see mulAddWindow for why
	//: len(s) bytes always suffice.
	mag := make([]byte, len(s))
	//: the live window starts empty at the far right and grows leftwards.
	start := len(mag)
	//: fold each digit into the running magnitude.
	for _, c := range s {
		//: reverse-lookup the digit.
		digit := reverse[c]
		//: base45NotMember signals a char outside the alphabet.
		if digit == base45NotMember {
			//: reject the whole input.
			return nil, false
		}
		//: magnitude = magnitude*radix + digit.
		start = mulAddWindow(mag, start, radix, digit)
	}
	//: prepend the recovered leading zero bytes.
	return assembleDecoded(zeros, mag[start:]), true
}

// assembleDecoded prepends zeros zero-bytes to buf (the decoded magnitude).
func assembleDecoded(zeros int, buf []byte) []byte {
	//: preallocate exactly the zero prefix + the magnitude bytes.
	out := make([]byte, 0, zeros+len(buf))
	//: emit one zero byte per recovered leading zero.
	for range zeros {
		//: leading zero byte of the decoded value.
		out = append(out, 0)
	}
	//: append the magnitude after the zero prefix.
	return append(out, buf...)
}
