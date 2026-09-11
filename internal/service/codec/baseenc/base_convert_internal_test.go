// Package baseenc — the big-endian base-conversion arithmetic. Every helper
// here is one step of a long division or a multiply-add, so the interesting
// cases are the boundaries: an empty magnitude, a carry that grows the buffer,
// and the leading-zero convention that carries information the arithmetic
// itself cannot represent.
package baseenc

import (
	"bytes"
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

// Test_divmodInPlace pins one long-division step. The buffer keeps its length
// on purpose — leading zeros appear and the caller trims them — so a helper
// that "helpfully" shortened it would break the caller's loop invariant.
func Test_divmodInPlace(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		buf     []byte
		radix   int
		wantBuf []byte
		wantRem int
	}
	tests := []tc{
		{"an empty magnitude", []byte{}, 58, []byte{}, 0},
		{"a value below the radix", []byte{0x01}, 58, []byte{0x00}, 1},
		{"a value equal to the radix", []byte{58}, 58, []byte{0x01}, 0},
		{"a two-byte magnitude", []byte{0x01, 0x00}, 58, []byte{0x00, 0x04}, 24},
		{"an all-zero magnitude", []byte{0x00, 0x00}, 58, []byte{0x00, 0x00}, 0},
		{"the maximum byte", []byte{0xFF}, 62, []byte{0x04}, 7},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		buf := slices.Clone(c.buf)

		got := divmodInPlace(buf, c.radix)

		if got != c.wantRem {
			t.Errorf("divmodInPlace(%x, %d) = %d, want %d", c.buf, c.radix, got, c.wantRem)
		}
		if !bytes.Equal(buf, c.wantBuf) {
			t.Errorf("divmodInPlace left %x, want %x", buf, c.wantBuf)
		}
		//: the length is the caller's loop invariant; the helper must not
		//: shorten it even when the quotient is all zeros.
		if len(buf) != len(c.buf) {
			t.Errorf("divmodInPlace changed the buffer length to %d, want %d", len(buf), len(c.buf))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_trimLeadingZeros pins the trim between divisions. An all-zero magnitude
// must come back empty, because that emptiness is what ends the caller's
// division loop — trimming to a single zero byte would spin forever.
func Test_trimLeadingZeros(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
		want []byte
	}
	tests := []tc{
		{"nothing to trim", []byte{0x01, 0x02}, []byte{0x01, 0x02}},
		{"one leading zero", []byte{0x00, 0x01}, []byte{0x01}},
		{"several leading zeros", []byte{0x00, 0x00, 0x00, 0x07}, []byte{0x07}},
		{"an all-zero magnitude empties", []byte{0x00, 0x00}, []byte{}},
		{"an empty input", []byte{}, []byte{}},
		{"an interior zero is kept", []byte{0x01, 0x00, 0x02}, []byte{0x01, 0x00, 0x02}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := trimLeadingZeros(c.in); !bytes.Equal(got, c.want) {
			t.Errorf("trimLeadingZeros(%x) = %x, want %x", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_mulAddWindow pins the multiply-add that grows the decode accumulator.
// The carry loop is the part worth pinning: a carry has to land at the new
// high end of the window, or the magnitude silently comes out byte-reversed
// at the top. The reserve check is the second half — the window grows into
// bytes the caller allocated but never wrote, so anything left of start must
// still be zero for mag[start:] to be the whole magnitude and nothing else.
func Test_mulAddWindow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		reserve int
		window  []byte
		radix   int
		add     int
		want    []byte
	}
	tests := []tc{
		{"an empty accumulator takes the addend", 1, []byte{}, 58, 7, []byte{0x07}},
		{"an empty accumulator with a zero addend stays empty", 1, []byte{}, 58, 0, []byte{}},
		{"no carry", 1, []byte{0x01}, 58, 0, []byte{58}},
		{"a carry grows the window", 1, []byte{0xFF}, 58, 0, []byte{0x39, 0xC6}},
		{"a full-width multiply grows the window", 2, []byte{0xFF, 0xFF}, 256, 0, []byte{0xFF, 0xFF, 0x00}},
		{"the addend lands in the low byte", 1, []byte{0x01}, 62, 5, []byte{67}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		mag := make([]byte, c.reserve+len(c.window))
		copy(mag[c.reserve:], c.window)

		start := mulAddWindow(mag, c.reserve, c.radix, c.add)

		if got := mag[start:]; !bytes.Equal(got, c.want) {
			t.Errorf("mulAddWindow(%x, radix=%d, add=%d) window = %x, want %x",
				c.window, c.radix, c.add, got, c.want)
		}
		//: the unconsumed reserve must stay zero — a stale byte there would
		//: silently widen the magnitude on the next growth.
		if !bytes.Equal(mag[:start], make([]byte, start)) {
			t.Errorf("mulAddWindow left %x in the reserve, want all zero", mag[:start])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
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
		buf   []byte
		want  []byte
	}
	tests := []tc{
		{"no zeros and no magnitude", 0, nil, []byte{}},
		{"only zeros", 3, nil, []byte{0x00, 0x00, 0x00}},
		{"only a magnitude", 0, []byte{0x01, 0x02}, []byte{0x01, 0x02}},
		{"zeros in front of the magnitude", 2, []byte{0x01}, []byte{0x00, 0x00, 0x01}},
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
