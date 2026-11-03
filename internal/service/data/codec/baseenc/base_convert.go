package baseenc

import "slices"

// Limb arithmetic (#898). The magnitude is held in 32-bit limbs and divided
// or multiplied by a power of the radix below 2^32 per step — five digits of
// base58 (58^5 = 656 356 768) or base62 (62^5 = 916 132 832) at once, through
// a uint64 accumulator — instead of by the radix one byte at a time: the same
// quadratic class, about twenty times fewer steps, and the division of the
// two shipped radices is by a constant, which the compiler turns into a
// multiplication. Bitcoin Core and the multiformats libraries do the same.
const (
	// limbBits is the width of one magnitude limb.
	limbBits uint = 32
	// limbBytes is limbBits in bytes.
	limbBytes int = 4
	// byteBits is the width of one byte of the magnitude, the step a limb is
	// written out by.
	byteBits int = 8
	// limbMask keeps the low limb of a uint64 accumulator.
	limbMask uint64 = 1<<limbBits - 1
	// base58Chunk is 58^5, the divisor of one base58 encode step.
	base58Chunk uint64 = 58 * 58 * 58 * 58 * 58
	// base62Chunk is 62^5, the divisor of one base62 encode step.
	base62Chunk uint64 = 62 * 62 * 62 * 62 * 62
	// maxDigitBits bounds log2 of any radix below 64, which sizes the decode
	// accumulator: radix^n < 2^(6n).
	maxDigitBits int = 6
)

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

// chunkOf returns the radix power one limb step divides or multiplies by, and
// how many digits it spans, for any radix in [2, 256).
func chunkOf(radix int) (power uint64, digits int) {
	power, digits = uint64(radix), 1
	//: the largest power that still fits a limb.
	for power*uint64(radix) <= limbMask {
		power *= uint64(radix)
		digits++
	}
	//: below 2^32.
	return power, digits
}

// divmodLimbs divides the big-endian limbs by power in place and returns the
// remainder. The two shipped radices divide by a constant.
func divmodLimbs(limbs []uint32, power uint64) uint64 {
	//: a constant divisor compiles to a multiplication.
	switch power {
	//: base58.
	case base58Chunk:
		return divmodLimbsBy58(limbs)
	//: base62.
	case base62Chunk:
		return divmodLimbsBy62(limbs)
	//: any other radix: the hardware division.
	default:
		return divmodLimbsBy(limbs, power)
	}
}

// divmodLimbsBy58 is divmodLimbs by 58^5, a constant.
func divmodLimbsBy58(limbs []uint32) uint64 {
	var rem uint64
	//: long division over the limbs, most-significant first.
	for i, limb := range limbs {
		acc := rem<<limbBits | uint64(limb)
		limbs[i] = uint32(acc / base58Chunk) //nolint:gosec // the quotient fits a limb
		rem = acc % base58Chunk
	}
	//: the next five digits.
	return rem
}

// divmodLimbsBy62 is divmodLimbs by 62^5, a constant.
func divmodLimbsBy62(limbs []uint32) uint64 {
	var rem uint64
	//: long division over the limbs, most-significant first.
	for i, limb := range limbs {
		acc := rem<<limbBits | uint64(limb)
		limbs[i] = uint32(acc / base62Chunk) //nolint:gosec // the quotient fits a limb
		rem = acc % base62Chunk
	}
	//: the next five digits.
	return rem
}

// divmodLimbsBy is divmodLimbs by any power below 2^32.
func divmodLimbsBy(limbs []uint32, power uint64) uint64 {
	var rem uint64
	//: long division over the limbs, most-significant first.
	for i, limb := range limbs {
		acc := rem<<limbBits | uint64(limb)
		limbs[i] = uint32(acc / power) //nolint:gosec // the quotient fits a limb
		rem = acc % power
	}
	//: the next digits.
	return rem
}

// bytesToLimbs packs the big-endian magnitude b into big-endian 32-bit limbs,
// the first holding the len(b) mod 4 most-significant bytes.
func bytesToLimbs(b []byte) []uint32 {
	limbs := make([]uint32, (len(b)+limbBytes-1)/limbBytes)
	end := len(b)
	//: fill from the least-significant limb.
	for i := len(limbs) - 1; i >= 0; i-- {
		begin := max(end-limbBytes, 0)
		var v uint32
		//: big-endian within the limb.
		for _, c := range b[begin:end] {
			v = v<<8 | uint32(c)
		}
		limbs[i] = v
		end = begin
	}
	//: packed.
	return limbs
}

// trimLeadingZeroLimbs returns limbs without its leading zero limbs. An
// all-zero magnitude comes back empty, which is what ends the encode loop.
func trimLeadingZeroLimbs(limbs []uint32) []uint32 {
	i := 0
	//: advance past every leading zero limb.
	for i < len(limbs) && limbs[i] == 0 {
		i++
	}
	//: slice off the zero prefix.
	return limbs[i:]
}

// encodeBaseN encodes raw as a big-endian base-conversion over alphabet
// (radix == len(alphabet)). Leading zero bytes become leading alphabet[0]
// chars. O(len(raw)²) in limb steps — caller must cap input at maxConvBytes.
func encodeBaseN(raw []byte, alphabet string, radix int) []byte {
	//: count leading zero bytes — they encode as leading alphabet[0].
	zeros := 0
	//: scan the zero-byte prefix.
	for zeros < len(raw) && raw[zeros] == 0 {
		//: one more leading zero to reproduce.
		zeros++
	}
	//: working magnitude, divided down in place; the caller's bytes are not.
	limbs := bytesToLimbs(raw[zeros:])
	power, chunk := chunkOf(radix)
	//: digits accumulate least-significant first.
	digits := make([]byte, 0, len(raw)*base45PairBytes+chunk)
	//: divide the magnitude down by the radix power until it is exhausted.
	for len(limbs) > 0 {
		rem := divmodLimbs(limbs, power)
		limbs = trimLeadingZeroLimbs(limbs)
		//: the chunk's digits, least-significant first.
		for range chunk {
			digits = append(digits, alphabet[rem%uint64(radix)])
			rem /= uint64(radix)
		}
	}
	//: the last chunk wrote zero digits above the magnitude's top digit,
	//: which is never zero: they are not part of the number.
	for len(digits) > 0 && digits[len(digits)-1] == alphabet[0] {
		digits = digits[:len(digits)-1]
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
// bytes. O(len(s)²) in limb steps — caller must cap input at maxConvBytes.
func decodeBaseN(s []byte, reverse *[base45TableSize]int, radix int) (raw []byte, ok bool) {
	//: count leading zero-digit chars (index 0) — they restore zero bytes.
	zeros := 0
	//: scan the zero-char prefix.
	for zeros < len(s) && reverse[s[zeros]] == 0 {
		//: one more leading zero byte to reproduce.
		zeros++
	}
	_, chunk := chunkOf(radix)
	//: little-endian limbs, one allocation for the whole fold: radix^n is
	//: below 2^(6n) for every radix below 64.
	limbs := make([]uint32, 0, len(s)*maxDigitBits/int(limbBits)+2)
	//: fold the digits in chunk by chunk: magnitude = magnitude*radix^k + v.
	for begin := 0; begin < len(s); begin += chunk {
		value, scale, valid := chunkValue(s[begin:min(begin+chunk, len(s))], reverse, radix)
		//: a char outside the alphabet rejects the whole input.
		if !valid {
			//: nothing decoded.
			return nil, false
		}
		limbs = mulAddLimbs(limbs, scale, value)
	}
	//: the magnitude's bytes, after the recovered leading zero bytes.
	return assembleDecoded(zeros, limbs), true
}

// chunkValue reads up to one chunk of digits: their value, and the radix
// power the magnitude is multiplied by to make room for them.
func chunkValue(digits []byte, reverse *[base45TableSize]int, radix int) (value, scale uint64, valid bool) {
	scale = 1
	//: most-significant digit first.
	for _, c := range digits {
		digit := reverse[c]
		//: base45NotMember signals a char outside the alphabet.
		if digit == base45NotMember {
			//: reject.
			return 0, 0, false
		}
		value = value*uint64(radix) + uint64(digit) //nolint:gosec // a table index, never negative
		scale *= uint64(radix)
	}
	//: below 2^32 both.
	return value, scale, true
}

// mulAddLimbs computes limbs = limbs*scale + add over little-endian limbs,
// scale and add below 2^32, growing by one limb when the carry needs it.
func mulAddLimbs(limbs []uint32, scale, add uint64) []uint32 {
	carry := add
	//: least-significant limb first; the carry stays below 2^32.
	for i, limb := range limbs {
		acc := uint64(limb)*scale + carry
		limbs[i] = uint32(acc & limbMask) //nolint:gosec // masked to a limb
		carry = acc >> limbBits
	}
	//: a new most-significant limb.
	if carry > 0 {
		limbs = append(limbs, uint32(carry)) //nolint:gosec // below 2^32 by construction
	}
	//: the grown magnitude.
	return limbs
}

// assembleDecoded writes zeros zero-bytes, then the magnitude held in the
// little-endian limbs as big-endian bytes without leading zeros.
func assembleDecoded(zeros int, limbs []uint32) []byte {
	//: preallocate exactly the zero prefix + every limb's bytes.
	out := make([]byte, 0, zeros+len(limbs)*limbBytes)
	//: the zero prefix, already zeroed by make.
	out = out[:zeros]
	//: most-significant limb first.
	for _, limb := range slices.Backward(limbs) {
		//: four bytes per limb, big-endian.
		for shift := int(limbBits) - byteBits; shift >= 0; shift -= byteBits {
			b := byte(limb >> uint(shift)) //nolint:gosec // one byte of the limb
			//: the magnitude starts at its first non-zero byte.
			if b == 0 && len(out) == zeros {
				continue
			}
			out = append(out, b)
		}
	}
	//: zero prefix, then the magnitude.
	return out
}
