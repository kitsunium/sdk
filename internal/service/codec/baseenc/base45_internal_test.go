// Package baseenc — the Base45 block codec (RFC 9285). The stdlib ships no
// Base45, so both directions live here and both are pinned against the RFC's
// own worked examples rather than against each other.
package baseenc

import (
	"bytes"
	"testing"
)

// Test_buildBase45Reverse pins the byte→index lookup the decoder consults. A
// byte absent from the alphabet must map to the sentinel: that is the only
// thing standing between a malformed input and a silently wrong decode, since
// a zero default would read every illegal byte as digit 0.
func Test_buildBase45Reverse(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		char    byte
		wantIdx int
	}
	tests := []tc{
		{"the first alphabet char", '0', 0},
		{"the last digit", '9', 9},
		{"the first letter", 'A', 10},
		{"the last letter", 'Z', 35},
		{"the space", ' ', 36},
		{"the last alphabet char", ':', 44},
		{"a lowercase letter is not a member", 'a', base45NotMember},
		{"a tab is not a member", '\t', base45NotMember},
		{"a high byte is not a member", 0xFF, base45NotMember},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		table := buildBase45Reverse()
		if got := table[c.char]; got != c.wantIdx {
			t.Errorf("table[%q] = %d, want %d", c.char, got, c.wantIdx)
		}
		//: the package-level table is built from the same function, so the two
		//: must agree — a divergence would make the tests meaningless.
		if base45Reverse[c.char] != table[c.char] {
			t.Errorf("the package table disagrees at %q: %d vs %d",
				c.char, base45Reverse[c.char], table[c.char])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: every byte outside the alphabet must be rejected, which is what makes
	//: an illegal character detectable at all.
	table := buildBase45Reverse()
	for b := range base45TableSize {
		if bytes.IndexByte([]byte(base45Alphabet), byte(b)) >= 0 {
			continue
		}
		if table[b] != base45NotMember {
			t.Errorf("table[%d] = %d for a byte outside the alphabet", b, table[b])
		}
	}
}

// Test_base45Triple pins the 3-char group reassembly. The digits are
// LITTLE-endian inside the group, which is the detail RFC 9285 readers most
// often get backwards, so the expected values are computed from the spec's
// place values rather than from the implementation.
func Test_base45Triple(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		want   int
		wantOK bool
	}
	tests := []tc{
		{"all zeros", "000", 0, true},
		{"only the least-significant digit", "100", 1, true},
		{"only the middle digit", "010", base45Radix, true},
		{"only the most-significant digit", "001", base45RadixSq, true},
		{"the maximum group", "::: ", 44 + 44*base45Radix + 44*base45RadixSq, true},
		{"a lowercase char is rejected", "a00", 0, false},
		{"an illegal char in the middle", "0a0", 0, false},
		{"an illegal char at the end", "00a", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		in := c.in[:base45GroupChars]
		got, ok := base45Triple(in[0], in[1], in[2])
		if ok != c.wantOK {
			t.Fatalf("base45Triple(%q) ok = %v, want %v", in, ok, c.wantOK)
		}
		if !ok {
			//: a rejected group must report zero, so a caller that ignored ok
			//: would at least not fabricate a plausible byte pair.
			if got != 0 {
				t.Errorf("base45Triple(%q) = %d beside the rejection", in, got)
			}
			return
		}
		if got != c.want {
			t.Errorf("base45Triple(%q) = %d, want %d", in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_base45Pair pins the 2-char trailing group, which uses the same
// little-endian ordering one place value shorter.
func Test_base45Pair(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		want   int
		wantOK bool
	}
	tests := []tc{
		{"all zeros", "00", 0, true},
		{"only the least-significant digit", "10", 1, true},
		{"only the most-significant digit", "01", base45Radix, true},
		{"the maximum pair", "::", 44 + 44*base45Radix, true},
		{"a lowercase char is rejected", "a0", 0, false},
		{"an illegal char at the end", "0a", 0, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := base45Pair(c.in[0], c.in[1])
		if ok != c.wantOK {
			t.Fatalf("base45Pair(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if !ok {
			if got != 0 {
				t.Errorf("base45Pair(%q) = %d beside the rejection", c.in, got)
			}
			return
		}
		if got != c.want {
			t.Errorf("base45Pair(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_encodeBase45 checks the encoder against the worked examples in RFC 9285
// §4.3. Pinning against the spec rather than against decodeBase45 is what makes
// the round-trip test below meaningful: two mutually consistent halves can
// still both be wrong.
func Test_encodeBase45(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"the RFC's AB example", "AB", "BB8"},
		{"the RFC's Hello!! example", "Hello!!", "%69 VD92EX0"},
		{"the RFC's base-45 example", "base-45", "UJCLQE7W581"},
		{"the RFC's ietf! example", "ietf!", "QED8WEX0"},
		{"a single zero byte", "\x00", "00"},
		{"an empty input", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(encodeBase45([]byte(c.in))); got != c.want {
			t.Errorf("encodeBase45(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_decodeBase45 pins the three rejection classes and the inverse. Each
// rejection matters for a different reason: an illegal length means the encoder
// that produced it was broken, an illegal char means the text was corrupted in
// transit, and an overflowing group means the value cannot fit the bytes it
// claims to represent — which a decoder that just truncated would turn into
// silently wrong data.
func Test_decodeBase45(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		in     string
		want   string
		wantOK bool
	}
	tests := []tc{
		{"the RFC's AB example", "BB8", "AB", true},
		{"the RFC's ietf! example", "QED8WEX0", "ietf!", true},
		{"an empty input", "", "", true},
		{"a single zero byte", "00", "\x00", true},
		//: len 4 → len%3 == 1, a length no encoder can produce.
		{"an impossible length class", "BB8B", "", false},
		{"a lowercase char", "ab8", "", false},
		//: 44 + 44*45 + 44*45² = 91124 > 0xFFFF, so the triple cannot be a pair.
		{"a triple overflowing its byte pair", ":::", "", false},
		//: 44 + 44*45 = 2024 > 0xFF, so the pair cannot be one byte.
		{"a pair overflowing its byte", "::", "", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := decodeBase45([]byte(c.in))
		if ok != c.wantOK {
			t.Fatalf("decodeBase45(%q) ok = %v, want %v", c.in, ok, c.wantOK)
		}
		if !ok {
			return
		}
		if string(got) != c.want {
			t.Errorf("decodeBase45(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestBase45RoundTrip confirms the two halves compose across every length class
// (even, odd, empty) and every byte value.
func TestBase45RoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
	}
	tests := []tc{
		{"an empty input", []byte{}},
		{"a lone zero byte", []byte{0x00}},
		{"the maximum byte", []byte{0xFF}},
		{"an even-length ASCII payload", []byte("AB")},
		{"an odd-length ASCII payload", []byte("Hello!!")},
		{"an ascending byte run", []byte{0x00, 0x01, 0x02, 0x03, 0x04}},
		{"an odd-length repeating payload", bytes.Repeat([]byte{0xAB}, 257)},
		{"every byte value once", func() []byte {
			all := make([]byte, base45TableSize)
			for i := range all {
				all[i] = byte(i)
			}
			return all
		}()},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		enc := encodeBase45(c.in)
		out, ok := decodeBase45(enc)
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
