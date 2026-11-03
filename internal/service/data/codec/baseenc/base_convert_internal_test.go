// Package baseenc — the big-endian base-conversion arithmetic. Every helper
// here is one step of a long division or a multiply-add, so the interesting
// cases are the boundaries: an empty magnitude, a carry that grows the buffer,
// and the leading-zero convention that carries information the arithmetic
// itself cannot represent.
package baseenc

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"
)

// Test_buildReverseTable pins the byte→index lookup every decoder consults.
// A byte absent from the alphabet must map to the sentinel, because that is
// the ONLY thing standing between a malformed input and a silently wrong
// decode: a zero default would read every illegal byte as digit 0.
func Test_buildReverseTable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		alphabet string
		member   byte
		wantIdx  int
		absent   byte
	}
	tests := []tc{
		{"base58 maps its first char to zero", base58Alphabet, base58Alphabet[0], 0, '0'},
		{"base58 maps its last char", base58Alphabet, base58Alphabet[len(base58Alphabet)-1], len(base58Alphabet) - 1, 'I'},
		{"base62 maps its first char to zero", base62Alphabet, base62Alphabet[0], 0, ' '},
		{"base62 maps its last char", base62Alphabet, base62Alphabet[len(base62Alphabet)-1], len(base62Alphabet) - 1, '/'},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := buildReverseTable(c.alphabet)

		if got := table[c.member]; got != c.wantIdx {
			t.Errorf("table[%q] = %d, want %d", c.member, got, c.wantIdx)
		}
		//: a non-member must be the sentinel, not a plausible digit.
		if got := table[c.absent]; got != base45NotMember {
			t.Errorf("table[%q] = %d for a non-member, want the not-member sentinel", c.absent, got)
		}
		//: every alphabet byte must round-trip through the table.
		for i := range c.alphabet {
			if table[c.alphabet[i]] != i {
				t.Errorf("table[%q] = %d, want %d", c.alphabet[i], table[c.alphabet[i]], i)
			}
		}
		//: and every byte NOT in the alphabet must be rejected, which is what
		//: makes an illegal character detectable at all.
		for b := range 256 {
			if bytes.IndexByte([]byte(c.alphabet), byte(b)) >= 0 {
				continue
			}
			if table[b] != base45NotMember {
				t.Errorf("table[%d] = %d for a byte outside the alphabet", b, table[b])
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

// Test_divmodLimbs pins one limb step: the constant-divisor paths of the two
// shipped radices and the general one agree, the limbs keep their length —
// the caller trims — and the remainder is the next chunk of digits.
func Test_divmodLimbs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		limbs     []uint32
		power     uint64
		wantLimbs []uint32
		wantRem   uint64
	}
	tests := []tc{
		{"an empty magnitude", []uint32{}, base58Chunk, []uint32{}, 0},
		{"a value below the power", []uint32{7}, base58Chunk, []uint32{0}, 7},
		{"a value equal to the power", []uint32{uint32(base62Chunk)}, base62Chunk, []uint32{1}, 0},
		{"two limbs", []uint32{1, 0}, base58Chunk, []uint32{0, 6}, 1<<32 - 6*base58Chunk},
		{"an all-zero magnitude keeps its length", []uint32{0, 0}, base62Chunk, []uint32{0, 0}, 0},
		{"a radix with no constant path", []uint32{1, 0}, 36 * 36 * 36 * 36 * 36 * 36, []uint32{0, 1}, 1<<32 - 36*36*36*36*36*36},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		limbs := slices.Clone(c.limbs)
		got := divmodLimbs(limbs, c.power)
		if got != c.wantRem || !slices.Equal(limbs, c.wantLimbs) {
			t.Errorf("divmodLimbs(%v, %d) = %v rem %d, want %v rem %d", c.limbs, c.power, limbs, got, c.wantLimbs, c.wantRem)
		}
		//: the general path must agree with the constant ones.
		general := slices.Clone(c.limbs)
		if rem := divmodLimbsBy(general, c.power); rem != got || !slices.Equal(general, limbs) {
			t.Errorf("divmodLimbsBy disagrees: %v rem %d", general, rem)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trimLeadingZeroLimbs pins the trim between divisions. An all-zero
// magnitude must come back empty, because that emptiness is what ends the
// caller's division loop — trimming to a single zero limb would spin forever.
func Test_trimLeadingZeroLimbs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in, want []uint32
	}{
		{[]uint32{1, 2}, []uint32{1, 2}},
		{[]uint32{0, 1}, []uint32{1}},
		{[]uint32{0, 0}, []uint32{}},
		{[]uint32{}, []uint32{}},
		{[]uint32{1, 0, 2}, []uint32{1, 0, 2}},
	} {
		if got := trimLeadingZeroLimbs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("trimLeadingZeroLimbs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// Test_bytesToLimbs pins the packing: the first limb holds the bytes that do
// not fill a whole one.
func Test_bytesToLimbs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   []byte
		want []uint32
	}{
		{nil, []uint32{}},
		{[]byte{0x01}, []uint32{0x01}},
		{[]byte{0x01, 0x02, 0x03, 0x04}, []uint32{0x01020304}},
		{[]byte{0x01, 0x02, 0x03, 0x04, 0x05}, []uint32{0x01, 0x02030405}},
	} {
		if got := bytesToLimbs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("bytesToLimbs(%x) = %x, want %x", c.in, got, c.want)
		}
	}
}

// Test_mulAddLimbs pins the multiply-add that grows the decode accumulator:
// a carry lands in a new most-significant limb, never lost.
func Test_mulAddLimbs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		limbs      []uint32
		scale, add uint64
		want       []uint32
	}{
		{"an empty accumulator takes the addend", nil, base58Chunk, 7, []uint32{7}},
		{"an empty accumulator with a zero addend stays empty", nil, base58Chunk, 0, nil},
		{"no carry", []uint32{1}, 58, 0, []uint32{58}},
		{"a carry grows the magnitude", []uint32{0xFFFFFFFF}, 2, 1, []uint32{0xFFFFFFFF, 1}},
		{"the addend lands in the low limb", []uint32{1, 1}, 62, 5, []uint32{67, 62}},
	} {
		if got := mulAddLimbs(slices.Clone(c.limbs), c.scale, c.add); !slices.Equal(got, c.want) {
			t.Errorf("%s: mulAddLimbs = %v, want %v", c.name, got, c.want)
		}
	}
}

// referenceEncodeBaseN is the byte-wise long division the limb arithmetic
// replaced (#898), kept as the oracle its output must equal byte for byte.
func referenceEncodeBaseN(raw []byte, alphabet string, radix int) []byte {
	zeros := 0
	for zeros < len(raw) && raw[zeros] == 0 {
		zeros++
	}
	buf := slices.Clone(raw[zeros:])
	var digits []byte
	for len(buf) > 0 {
		rem := 0
		for i := range buf {
			acc := rem*256 + int(buf[i])
			buf[i] = byte(acc / radix)
			rem = acc % radix
		}
		digits = append(digits, alphabet[rem])
		for len(buf) > 0 && buf[0] == 0 {
			buf = buf[1:]
		}
	}
	out := bytes.Repeat([]byte{alphabet[0]}, zeros)
	slices.Reverse(digits)
	return append(out, digits...)
}

// referenceDecodeBaseN is the byte-wise multiply-add the limb arithmetic
// replaced, kept as the oracle.
func referenceDecodeBaseN(s []byte, reverse *[base45TableSize]int, radix int) ([]byte, bool) {
	zeros := 0
	for zeros < len(s) && reverse[s[zeros]] == 0 {
		zeros++
	}
	var mag []byte
	for _, c := range s {
		digit := reverse[c]
		if digit == base45NotMember {
			return nil, false
		}
		carry := digit
		for i := len(mag) - 1; i >= 0; i-- {
			acc := int(mag[i])*radix + carry
			mag[i] = byte(acc)
			carry = acc / 256
		}
		for carry > 0 {
			mag = append([]byte{byte(carry)}, mag...)
			carry /= 256
		}
	}
	return append(make([]byte, zeros), mag...), true
}

// TestLimbConversionMatchesLongDivision is #898's identity gate: the limb
// arithmetic writes and reads exactly what the byte-wise long division did,
// for both radices, on every length from 0 to 95 bytes and in widening steps
// up to 600 — each limb-boundary residue many times over — with and without
// leading zeros, and at the all-0xFF magnitudes where the top chunk carries
// the most digits.
func TestLimbConversionMatchesLongDivision(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(898, 58))
	for _, v := range []struct {
		alphabet string
		radix    int
		reverse  *[base45TableSize]int
	}{
		{base58Alphabet, base58Radix, &base58Reverse},
		{base62Alphabet, base62Radix, &base62Reverse},
	} {
		for size := 0; size <= 600; size += 1 + 6*(size/96) {
			raw := make([]byte, size)
			for i := range raw {
				raw[i] = byte(rng.Uint32())
			}
			inputs := [][]byte{raw, bytes.Repeat([]byte{0xFF}, size)}
			if size > 2 {
				inputs = append(inputs, append([]byte{0, 0}, raw[2:]...))
			}
			for _, in := range inputs {
				got := encodeBaseN(in, v.alphabet, v.radix)
				want := referenceEncodeBaseN(in, v.alphabet, v.radix)
				if !bytes.Equal(got, want) {
					t.Fatalf("radix %d, %d bytes: encode %q, long division %q", v.radix, size, got, want)
				}
				back, ok := decodeBaseN(got, v.reverse, v.radix)
				wantBack, wantOK := referenceDecodeBaseN(got, v.reverse, v.radix)
				if !ok || !wantOK || !bytes.Equal(back, wantBack) || !bytes.Equal(back, in) {
					t.Fatalf("radix %d, %d bytes: decode %x (%v), long division %x (%v), want %x", v.radix, size, back, ok, wantBack, wantOK, in)
				}
			}
		}
	}
}

// Test_assembleBaseN pins the final ordering. Digits arrive least-significant
// first and the leading zero-chars must NOT be caught up in the reversal — a
// flip of the whole buffer would move them to the end, where they mean nothing.
func Test_assembleBaseN(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		zeroChar byte
		zeros    int
		digits   []byte
		want     string
	}
	tests := []tc{
		{"no zeros, no digits", '1', 0, nil, ""},
		{"only leading zeros", '1', 3, nil, "111"},
		{"digits are reversed", '1', 0, []byte("abc"), "cba"},
		{"zeros stay in front of the reversal", '1', 2, []byte("abc"), "11cba"},
		{"the base62 zero char", '0', 1, []byte("xy"), "0yx"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		digits := slices.Clone(c.digits)
		if got := string(assembleBaseN(c.zeroChar, c.zeros, digits)); got != c.want {
			t.Errorf("assembleBaseN(%q, %d, %q) = %q, want %q",
				c.zeroChar, c.zeros, c.digits, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_assembleDecoded pins the mirror step on the way back: the recovered
// zero bytes go in front of the magnitude, never after it.
func Test_assembleDecoded(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		zeros int
		buf   []uint32
		want  []byte
	}
	tests := []tc{
		{"no zeros and no magnitude", 0, nil, []byte{}},
		{"only zeros", 3, nil, []byte{0x00, 0x00, 0x00}},
		{"only a magnitude", 0, []uint32{0x0102}, []byte{0x01, 0x02}},
		{"zeros in front of the magnitude", 2, []uint32{0x01}, []byte{0x00, 0x00, 0x01}},
		{"limbs little-endian, bytes big-endian", 1, []uint32{0x05060708, 0x01}, []byte{0x00, 0x01, 0x05, 0x06, 0x07, 0x08}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := assembleDecoded(c.zeros, c.buf); !bytes.Equal(got, c.want) {
			t.Errorf("assembleDecoded(%d, %x) = %x, want %x", c.zeros, c.buf, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_encodeBaseN pins the leading-zero convention, which is the one piece of
// information the arithmetic cannot carry: an integer has no notion of how many
// zero bytes preceded it, so each one has to map to an explicit zero-digit
// character or the decode comes back short.
func Test_encodeBaseN(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		in       []byte
		alphabet string
		radix    int
		want     string
	}
	tests := []tc{
		{"base58 empty input", nil, base58Alphabet, base58Radix, ""},
		{"base58 a lone zero byte", []byte{0x00}, base58Alphabet, base58Radix, "1"},
		{"base58 two zeros then a one", []byte{0x00, 0x00, 0x01}, base58Alphabet, base58Radix, "112"},
		{"base62 a lone zero byte", []byte{0x00}, base62Alphabet, base62Radix, "0"},
		{"base62 an all-zero input", []byte{0x00, 0x00}, base62Alphabet, base62Radix, "00"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		in := slices.Clone(c.in)

		got := string(encodeBaseN(in, c.alphabet, c.radix))

		if got != c.want {
			t.Errorf("encodeBaseN(%x) = %q, want %q", c.in, got, c.want)
		}
		//: the caller's bytes must survive; the division works on a copy.
		if !bytes.Equal(in, c.in) {
			t.Errorf("encodeBaseN mutated its input to %x, want %x", in, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_decodeBaseN pins the inverse and the rejection. A character outside the
// alphabet must fail the whole input rather than decode as digit zero — Base58
// deliberately excludes 0, O, I and l precisely because a human transcribing an
// identifier confuses them, and silently accepting one would hand back the
// wrong bytes with no error.
func Test_decodeBaseN(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []byte
		reverse *[base45TableSize]int
		radix   int
		want    []byte
		wantOK  bool
	}
	tests := []tc{
		{"base58 an empty input", nil, &base58Reverse, base58Radix, []byte{}, true},
		{"base58 a lone zero char", []byte("1"), &base58Reverse, base58Radix, []byte{0x00}, true},
		{"base58 zeros then a digit", []byte("112"), &base58Reverse, base58Radix, []byte{0x00, 0x00, 0x01}, true},
		{"base58 rejects the excluded chars", []byte("0OIl"), &base58Reverse, base58Radix, nil, false},
		{"base62 rejects a space", []byte("ab cd"), &base62Reverse, base62Radix, nil, false},
		{"base62 a lone zero char", []byte("0"), &base62Reverse, base62Radix, []byte{0x00}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := decodeBaseN(c.in, c.reverse, c.radix)
		if ok != c.wantOK {
			t.Fatalf("decodeBaseN(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if !ok {
			//: a rejection must hand back nothing, or a caller checking only
			//: the bytes would use a partial decode.
			if got != nil {
				t.Errorf("decodeBaseN(%q) returned %x beside the rejection", c.in, got)
			}
			return
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("decodeBaseN(%q) = %x, want %x", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBaseConversion_RoundTrip confirms decodeBaseN inverts encodeBaseN for
// both Base58 and Base62 across payloads that exercise leading zeros, the empty
// input, and multi-byte magnitudes. The per-function tests above pin the steps;
// this pins that the steps compose.
func TestBaseConversion_RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		alphabet string
		radix    int
		reverse  *[base45TableSize]int
		in       []byte
	}
	type payload struct {
		label string
		bytes []byte
	}
	inputs := []payload{
		{"an empty input", []byte{}},
		{"a lone zero byte", []byte{0x00}},
		{"three zero bytes", []byte{0x00, 0x00, 0x00}},
		{"the maximum byte", []byte{0xFF}},
		{"leading zeros then a one", []byte{0x00, 0x00, 0x01}},
		{"an ASCII payload", []byte("Hello World")},
		{"a long repeating payload", bytes.Repeat([]byte{0xAB, 0x00, 0x01}, 64)},
	}
	var tests []tc
	for _, v := range []struct {
		name     string
		alphabet string
		radix    int
		reverse  *[base45TableSize]int
	}{
		{"base58", base58Alphabet, base58Radix, &base58Reverse},
		{"base62", base62Alphabet, base62Radix, &base62Reverse},
	} {
		for _, in := range inputs {
			tests = append(tests, tc{v.name + " with " + in.label, v.alphabet, v.radix, v.reverse, in.bytes})
		}
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		enc := encodeBaseN(c.in, c.alphabet, c.radix)
		out, ok := decodeBaseN(enc, c.reverse, c.radix)
		if !ok {
			t.Fatalf("decode rejected %q, which encode had just produced from %x", enc, c.in)
		}
		if !bytes.Equal(out, c.in) {
			t.Errorf("the round trip turned %x into %x (via %q)", c.in, out, enc)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
