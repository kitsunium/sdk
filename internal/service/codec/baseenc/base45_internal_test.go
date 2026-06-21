package baseenc

import (
	"bytes"
	"testing"
)

// TestEncodeBase45_RFCVectors checks encodeBase45 against the worked
// examples in RFC 9285 §4.3, plus a single-byte (odd-length) case.
func TestEncodeBase45_RFCVectors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"AB", "AB", "BB8"},
		{"Hello!!", "Hello!!", "%69 VD92EX0"},
		{"base-45", "base-45", "UJCLQE7W581"},
		{"ietf!", "ietf!", "QED8WEX0"},
		{"single-byte", "\x00", "00"},
		{"empty", "", ""},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got := string(encodeBase45([]byte(tc.in)))
		//: the encoding must match the RFC table exactly.
		if got != tc.want {
			t.Errorf("encodeBase45(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestDecodeBase45_RoundTrip confirms decodeBase45 inverts encodeBase45
// across byte payloads of every length class (even, odd, empty).
func TestDecodeBase45_RoundTrip(t *testing.T) {
	t.Parallel()
	inputs := [][]byte{
		{},
		{0x00},
		{0xFF},
		[]byte("AB"),
		[]byte("Hello!!"),
		{0x00, 0x01, 0x02, 0x03, 0x04},
		bytes.Repeat([]byte{0xAB}, 257),
	}
	runCase := func(t *testing.T, in []byte) {
		t.Helper()
		out, ok := decodeBase45(encodeBase45(in))
		//: decode must succeed and reproduce the original bytes.
		if !ok || !bytes.Equal(out, in) {
			t.Errorf("round-trip failed for %x: got %x ok=%v", in, out, ok)
		}
	}
	for _, in := range inputs {
		t.Run("", func(t *testing.T) { t.Parallel(); runCase(t, in) })
	}
}

// TestDecodeBase45_Malformed verifies the three rejection classes: an
// illegal length (len%3 == 1), a char outside the alphabet, and a group
// whose value overflows the byte(s) it must represent.
func TestDecodeBase45_Malformed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{
		{"bad-length-class", "BB8B"},     // len 4 → len%3 == 1
		{"illegal-char", "ab8"},          // lowercase letters are not in the alphabet
		{"triple-overflows-pair", "ZZZ"}, // 35+35*45+35*45² = 72485 > 0xFFFF
		{"pair-overflows-byte", "ZZ"},    // 35+35*45 = 1610 > 0xFF
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, ok := decodeBase45([]byte(tc.in))
		//: every case must be rejected.
		if ok {
			t.Errorf("decodeBase45(%q) accepted malformed input", tc.in)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
