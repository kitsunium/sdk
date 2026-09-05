package baseenc

import (
	"bytes"
	"testing"
)

// TestBaseConversion_RoundTrip confirms decodeBaseN inverts encodeBaseN for
// both Base58 and Base62 across payloads that exercise leading zeros, the
// empty input, and multi-byte magnitudes.
func TestBaseConversion_RoundTrip(t *testing.T) {
	t.Parallel()
	type variantCfg struct {
		name     string
		alphabet string
		radix    int
		reverse  *[base45TableSize]int
	}
	variants := []variantCfg{
		{"base58", base58Alphabet, base58Radix, &base58Reverse},
		{"base62", base62Alphabet, base62Radix, &base62Reverse},
	}
	inputs := [][]byte{
		{},
		{0x00},
		{0x00, 0x00, 0x00},
		{0xFF},
		{0x00, 0x00, 0x01},
		[]byte("Hello World"),
		bytes.Repeat([]byte{0xAB, 0x00, 0x01}, 64),
	}
	runCase := func(t *testing.T, v variantCfg, in []byte) {
		t.Helper()
		enc := encodeBaseN(in, v.alphabet, v.radix)
		out, ok := decodeBaseN(enc, v.reverse, v.radix)
		//: decode must succeed and reproduce the original bytes.
		if !ok || !bytes.Equal(out, in) {
			t.Errorf("%s round-trip failed for %x: enc=%q out=%x ok=%v", v.name, in, enc, out, ok)
		}
	}
	for _, v := range variants {
		for _, in := range inputs {
			t.Run(v.name, func(t *testing.T) { t.Parallel(); runCase(t, v, in) })
		}
	}
}

// TestBaseConversion_LeadingZeros pins the leading-zero convention: each
// zero byte maps to one alphabet[0] char ('1' for Base58, '0' for Base62).
func TestBaseConversion_LeadingZeros(t *testing.T) {
	t.Parallel()
	//: a lone zero byte is exactly one zero-digit char.
	if got := string(encodeBaseN([]byte{0x00}, base58Alphabet, base58Radix)); got != "1" {
		t.Errorf("base58 [0x00] = %q, want \"1\"", got)
	}
	//: two leading zeros + a 0x01 byte → "11" + "2".
	if got := string(encodeBaseN([]byte{0x00, 0x00, 0x01}, base58Alphabet, base58Radix)); got != "112" {
		t.Errorf("base58 [0,0,1] = %q, want \"112\"", got)
	}
	//: Base62's zero char is '0'.
	if got := string(encodeBaseN([]byte{0x00}, base62Alphabet, base62Radix)); got != "0" {
		t.Errorf("base62 [0x00] = %q, want \"0\"", got)
	}
}

// TestBaseConversion_Malformed verifies a char outside the alphabet is
// rejected (Base58 excludes 0, O, I, l by construction).
func TestBaseConversion_Malformed(t *testing.T) {
	//: '0' is not in the Base58 alphabet.
	if _, ok := decodeBaseN([]byte("0OIl"), &base58Reverse, base58Radix); ok {
		t.Error("base58 accepted excluded chars 0OIl")
	}
	//: a space is in neither alphabet.
	if _, ok := decodeBaseN([]byte("ab cd"), &base62Reverse, base62Radix); ok {
		t.Error("base62 accepted an illegal space char")
	}
}
